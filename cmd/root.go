package cmd

import (
	"errors"
	"os"
	"strconv"
	"strings"

	"github.com/rtxnik/workspace-cli/internal/output"
	"github.com/spf13/cobra"
)

var version = "dev"

// logoLines is the logo ws --version prints above the version on a UTF-8
// terminal.
var logoLines = []string{"╦ ╦╔═╗", "║║║╚═╗", "╚╩╝╚═╝"}

// versionTemplate is the --version template for s, the stream the version
// is written to. It is the version alone unless s is a terminal in the UTF-8
// glyph mode; there the logo comes first, painted RoleAccent — without
// colour under NO_COLOR. Piped, the output is exactly "ws <version>\n", so
// `ws --version | head -1` is the version.
func versionTemplate(s *output.Stream) string {
	const versionLine = "ws {{.Version}}\n"
	if !s.IsTTY() || s.Mode() != output.GlyphUTF8 {
		return versionLine
	}
	var b strings.Builder
	for _, line := range logoLines {
		b.WriteString(s.Style(output.RoleAccent).Render(line) + "\n")
	}
	return b.String() + versionLine
}

var rootCmd = &cobra.Command{
	Use:   "ws",
	Short: "Workspace manager for DevPod",
	Long:  "ws — workspace manager CLI for DevPod environments with proxy support.",
	// The root prints every error itself — Execute, below — so cobra must
	// not: with both printing, each error appeared twice, as "Error: <msg>"
	// and again as the ✗ line. The same goes for usage. cobra printed the
	// whole usage block before the error; Execute prints a synopsis and a
	// pointer to --help after it, for a usage error only (usageTarget).
	SilenceErrors: true,
	SilenceUsage:  true,
	// cobra would otherwise append its "Did you mean this?" list to the text
	// of its unknown-command error; Execute prints the suggestions as a line
	// of their own.
	DisableSuggestions: true,
}

// cliErrorWithExit is the leaf-level error type that surfaces a non-default
// exit code. Per CONTEXT D-18 + envelope.go MapErrorCodeToExitCode, exit
// codes 0-7 map to the documented MCP error codes; a leaf wraps the failing
// envelope into a cliErrorWithExit{code, msg} and the process exits with code.
//
// A non-empty msg is printed once, by the root, like any other error. An
// empty msg, unwrapped, prints nothing: the command has already printed
// everything the operator needs — vault-health-score's score on stdout, a
// status report — and the exit code is the rest of its interface.
type cliErrorWithExit struct {
	code int
	msg  string
}

func (e *cliErrorWithExit) Error() string { return e.msg }

// run is the root's error protocol. Given what the command tree returned, it
// decides what the root prints and what the process exits with; msg is
// printed through output.Fail, and an empty msg prints nothing. Four branches
// and nothing else:
//
//	nil                                     -> "", 0
//	*cliErrorWithExit, err.Error() == ""    -> "", cerr.code
//	*cliErrorWithExit, err.Error() != ""    -> err.Error(), cerr.code
//	any other error                         -> err.Error(), 1
//
// The two *cliErrorWithExit branches test and print err.Error(), not
// cerr.msg: errors.As also finds one that another error wraps, and cerr.msg
// would drop the wrapper's text. Unwrapped, the two are the same string.
func run(err error) (msg string, code int) {
	if err == nil {
		return "", 0
	}
	var cerr *cliErrorWithExit
	if errors.As(err, &cerr) {
		return err.Error(), cerr.code
	}
	return err.Error(), 1
}

// usageError is an argument error: what the operator typed was wrong, and
// the remedy is cmd's synopsis. Its text is the text of the error it
// carries, so wrapping an error in it changes no message. suggestions are
// the commands to offer for an unknown subcommand.
type usageError struct {
	cmd         *cobra.Command
	err         error
	suggestions []string
}

func (e *usageError) Error() string { return e.err.Error() }
func (e *usageError) Unwrap() error { return e.err }

// flagError is the root's FlagErrorFunc, which every command inherits: a
// flag that fails to parse is a usage error of the command that parsed it.
func flagError(cmd *cobra.Command, err error) error {
	return &usageError{cmd: cmd, err: err}
}

// usageTarget reports whether err, returned by ExecuteC for cmd, is a usage
// error, and if it is, the command its usage lines name and the suggestions
// to print. Two things make one, and nothing else does:
//
//  1. err is, or wraps, a *usageError, which names its own command;
//  2. cmd is a leaf whose SilenceUsage is still false. Its body had not
//     started — every body sets SilenceUsage first, and a hook sets it only
//     on its own error path (TestSilenceUsageForms) — so cobra rejected its
//     arguments, or its required or grouped flags.
//
// A command with subcommands has no body; any other error it returns — a
// failed write of the --version output — is a runtime error.
func usageTarget(cmd *cobra.Command, err error) (target *cobra.Command, suggestions []string, ok bool) {
	var ue *usageError
	if errors.As(err, &ue) {
		return ue.cmd, ue.suggestions, true
	}
	if err != nil && cmd != nil && !cmd.HasSubCommands() && !cmd.SilenceUsage {
		return cmd, nil, true
	}
	return nil, nil, false
}

// Execute runs the command tree. An error that reaches it is printed at most
// once, through output.Fail, and the process exits with the code run chose.
// A usage error gets its usage lines under the ✗ line.
func Execute() {
	// Built here rather than at package init, so that it is built for the
	// stream --version writes to, once that stream has been resolved.
	rootCmd.SetVersionTemplate(versionTemplate(output.Out()))
	cmd, err := execute(rootCmd)
	msg, code := run(err)
	if msg != "" {
		output.Fail(msg)
		if target, suggestions, ok := usageTarget(cmd, err); ok {
			printUsageLines(target, suggestions)
		}
	}
	if code != 0 {
		os.Exit(code)
	}
}

// execute runs root and returns the command that failed and the error the
// protocol reads: ExecuteC's own, or, when that is nil, a word left over
// under a command with subcommands whose help cobra answered in this run;
// and cobra's rejection of an unknown root command, typed as a usage error
// of the root.
//
// A command with subcommands is not runnable, so cobra answers it with
// flag.ErrHelp before it validates arguments or runs a pre-run hook, and a
// word left over under it reaches the help function, with --help or
// without. That word names no subcommand, so for this run the root's help
// function is wrapped: where cobra answers with help it passes the command
// line, and the wrapper records the word, read from the flag set cobra has
// just parsed, instead of printing the help. cmd.Help() passes no command
// line, and then the flag set may still hold the words of an earlier run,
// so they are not read. Without --help, cobra answers --version before it
// turns to a command that is not runnable, and without calling the help
// function, so a word left over beside --version is left alone.
func execute(root *cobra.Command) (*cobra.Command, error) {
	var stray error
	help := root.HelpFunc()
	root.SetHelpFunc(func(c *cobra.Command, args []string) {
		if word, ok := strayWordOf(c); ok && len(args) > 0 {
			stray = unknownSubcommand(c, word)
			return
		}
		help(c, args)
	})
	defer root.SetHelpFunc(help)
	cmd, err := root.ExecuteC()
	if err == nil {
		err = stray
	}
	if err != nil && !cmd.HasParent() {
		if ue, ok := unknownRootCommand(root, err); ok {
			err = ue
		}
	}
	return cmd, err
}

// printUsageLines prints a usage error's lines under its ✗ line, through
// output.Detail: the suggestions, when there are any, then target's
// synopsis and where to read its help.
func printUsageLines(target *cobra.Command, suggestions []string) {
	if len(suggestions) > 0 {
		output.Detail("Did you mean: " + strings.Join(suggestions, ", ") + "?")
	}
	output.Detail("Usage: " + synopsis(target))
	output.Detail("Run '" + target.CommandPath() + " --help' for usage.")
}

// isUnknownCommand reports whether err carries cobra's "unknown command"
// text. cobra builds that error with fmt.Errorf and exports no type for it,
// so its text is the only handle.
//
// Two different errors carry that text: the one the root raises for a command
// it cannot find (args.go legacyArgs), and the one cobra.NoArgs raises for an
// extra argument (args.go NoArgs). Only the first can come back from a
// command with no parent, which is how execute tells them apart; the second
// is a leaf's, and usageTarget classifies it by its leaf.
func isUnknownCommand(err error) bool {
	return err != nil && strings.HasPrefix(err.Error(), "unknown command ")
}

// unknownRootCommand types cobra's rejection of an unknown command at the
// root. With the root's Args nil, cobra rejects the word inside Find, before
// it looks at --help or --version, so `ws nosuch --help` stays an error.
func unknownRootCommand(root *cobra.Command, err error) (*usageError, bool) {
	word, ok := unknownRootWord(err)
	if !ok {
		return nil, false
	}
	return &usageError{cmd: root, err: err, suggestions: suggestionsFor(root, word)}, true
}

// unknownRootWord reads the word back out of that rejection. With
// DisableSuggestions its message is exactly `unknown command "<word>" for
// "ws"`, and cobra quotes the word with %q: strconv.QuotedPrefix finds where
// the quoted word ends, and strconv.Unquote undoes its escaping.
func unknownRootWord(err error) (string, bool) {
	if !isUnknownCommand(err) {
		return "", false
	}
	quoted, qerr := strconv.QuotedPrefix(strings.TrimPrefix(err.Error(), "unknown command "))
	if qerr != nil {
		return "", false
	}
	word, qerr := strconv.Unquote(quoted)
	if qerr != nil {
		return "", false
	}
	return word, true
}

// suggestionsFor is cmd.SuggestionsFor at cobra's default distance of 2.
// SuggestionsFor does not apply that default itself; only its unexported
// caller does, and without it the Levenshtein half matches nothing.
func suggestionsFor(cmd *cobra.Command, word string) []string {
	if cmd.SuggestionsMinimumDistance <= 0 {
		cmd.SuggestionsMinimumDistance = 2
	}
	return cmd.SuggestionsFor(word)
}

func init() {
	rootCmd.Version = version
	rootCmd.CompletionOptions.DisableDefaultCmd = true
	// Every command inherits --json, and only some of them read it; the
	// usage says so, rather than promise an effect a command does not have.
	rootCmd.PersistentFlags().Bool("json", false, "Output in JSON format, where the command supports it")
	rootCmd.SetHelpFunc(helpFunc)
	rootCmd.SetUsageFunc(usageFunc)
	rootCmd.SetFlagErrorFunc(flagError)
}
