package cmd

import (
	"errors"
	"strings"

	"github.com/rtxnik/workspace-cli/internal/output"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// confirmNoTerminal is the title of the Problem a confirmation gives when it
// could not ask.
const confirmNoTerminal = "Cannot ask for confirmation: no terminal"

// promptReadyFn reports whether a prompt may ask, as output.PromptReady does.
// It is a seam for the commands that ask it themselves, before work that
// precedes their prompt — the vault doctor before its checks — so that the
// tests, whose stdin is the null device, can drive them past it.
var promptReadyFn = output.PromptReady

// promptProblem is what a command returns when its prompt could not ask: err
// is output.ErrNoTTY's, and the Problem says what could not happen (title),
// names the stream that is not a terminal (its cause), and gives as its step
// the command the operator ran with skip — the flag that skips the prompt.
// The Problem wraps err, so errors.Is(err, output.ErrNoTTY) still holds at
// the root. A cancellation and any other error pass through unchanged.
func promptProblem(cmd *cobra.Command, args []string, err error, title, skip string) error {
	if !errors.Is(err, output.ErrNoTTY) {
		return err
	}
	return &output.ProblemError{P: output.Problem{
		Title: title,
		Cause: err.Error(),
		Steps: []output.Remedy{skipStep(cmd, args, skip)},
	}, Err: err}
}

// skipStep is the step that re-runs cmd's invocation with skip, given to the
// layer as shell words so that it pastes back as that invocation. When a
// word would not show on a terminal as it is, no word is echoed and the step
// names the flag alone.
func skipStep(cmd *cobra.Command, args []string, skip string) output.Remedy {
	words := remedyWords(cmd, args, skip)
	quoted := make([]string, 0, len(words))
	for _, w := range words {
		q, ok := output.ShellQuote(w)
		if !ok {
			return output.Remedy{Label: "Re-run with", Cmd: "--" + skip}
		}
		quoted = append(quoted, q)
	}
	return output.Remedy{Label: "Skip the prompt", Words: quoted}
}

// remedyWords is cmd's invocation as cobra parsed it, in a canonical form,
// with skip set: the command's path — so an alias reads as the command's
// name — then each flag of the leaf, inherited ones included, that the parse
// set, in pflag's sorted order, `--name` for a boolean set to true and
// `--name=<value>` for anything else, then `--skip`, then the positional
// arguments, after a `--` when any of them begins with a dash. The flags are
// read with VisitAll and Changed, not Visit: a flag set's record of the
// flags it parsed is never cleared, so in one process — the in-process tests
// — Visit also reports the flags of an earlier run.
func remedyWords(cmd *cobra.Command, args []string, skip string) []string {
	words := strings.Fields(cmd.CommandPath())
	cmd.Flags().VisitAll(func(f *pflag.Flag) {
		switch {
		case !f.Changed || f.Name == skip:
		case f.Value.Type() == "bool" && f.Value.String() == "true":
			words = append(words, "--"+f.Name)
		default:
			words = append(words, "--"+f.Name+"="+f.Value.String())
		}
	})
	words = append(words, "--"+skip)
	for _, a := range args {
		if strings.HasPrefix(a, "-") {
			words = append(words, "--")
			break
		}
	}
	return append(words, args...)
}
