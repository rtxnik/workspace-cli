package output

import (
	"strings"
	"unicode"
)

// ShellQuote writes word for a POSIX shell to read back as exactly that
// word, and reports whether a terminal shows it as it is. A word it does not
// show must not be echoed at all: a remedy then names its flag alone.
//
// A word made only of letters, digits and _./:=@%+,- and not beginning with
// = is written as it is — zsh replaces a word that begins with = by a
// command's path. Every other word, the empty one included, is written in
// single quotes, each single quote inside it ended, escaped with a backslash
// and reopened.
//
// A terminal does not show a word as it is when the layer's Sanitise would
// drop or change anything in it — a C0 control, DEL, a C1 control, an escape
// sequence and its payload, a byte that is not valid UTF-8 (which Sanitise
// writes as U+FFFD) — or when it holds a tab, a newline or a Unicode format
// character (category Cf, the bidirectional controls among them), which make
// a step read differently from what it runs.
func ShellQuote(word string) (string, bool) {
	if Sanitise(word) != word || strings.ContainsAny(word, "\t\n") || strings.IndexFunc(word, isFormat) >= 0 {
		return "", false
	}
	if word != "" && word[0] != '=' && strings.IndexFunc(word, notBare) < 0 {
		return word, true
	}
	return "'" + strings.ReplaceAll(word, "'", `'\''`) + "'", true
}

// bareChars are the characters a word may hold and be written unquoted.
const bareChars = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789_./:=@%+,-"

func notBare(r rune) bool { return !strings.ContainsRune(bareChars, r) }

func isFormat(r rune) bool { return unicode.Is(unicode.Cf, r) }
