package cmd

// vault_backup_verify.go — `ws vault backup-verify` leaf (CLI-06 + HARD-09).
//
// Reads the per-ISO-week backup-verify log written by Phase 21c HARD-07 cron
// (~/projects/vault-ai/_tooling/logs/backup-verify-{YYYY-Www}.jsonl) and
// exits 0 on green, 1 on rot, 4 on missing/stale.
//
// Graceful fallback per CONTEXT D-26: Phase 21c is NOT YET shipped, so the
// log file likely does not exist at Phase 18 ship time. Exit 4 with an
// explicit "Phase 21c HARD-07 not yet shipped" diagnostic so the operator
// cannot mistake an absent log for a green backup.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// Test seams for the three filesystem operations the leaf depends on.
// Production wires them to stdlib; unit tests override with closures that
// emulate Phase 21c log absence / staleness / green / rot scenarios.
var (
	backupVerifyGlobFn = filepath.Glob
	backupVerifyStatFn = os.Stat
	backupVerifyReadFn = os.ReadFile
)

// backupVerifyStaleAfter is how long the leaf will trust a backup-verify log
// before flagging it stale per CONTEXT D-26. Cron writes weekly; 14 days
// allows one missed run before alarming.
const backupVerifyStaleAfter = 14 * 24 * time.Hour

// backupVerifyEntry is the per-row jsonl shape written by the Phase 21c
// HARD-07 cron. Only the fields the CLI consumes are decoded.
type backupVerifyEntry struct {
	Timestamp string `json:"ts,omitempty"`
	Outcome   string `json:"outcome,omitempty"`
	Details   string `json:"details,omitempty"`
}

// resolveBackupVerifyLogDir returns the directory backup-verify-*.jsonl logs
// live in per CONTEXT D-26. Honors VAULT_AI_REPO_ROOT then $HOME/projects/vault-ai.
func resolveBackupVerifyLogDir() (string, error) {
	root := os.Getenv("VAULT_AI_REPO_ROOT")
	if root == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		root = filepath.Join(home, "projects", "vault-ai")
	}
	return filepath.Join(root, "_tooling", "logs"), nil
}

// pickLatestBackupVerifyLog returns the absolute path with the most recent
// mtime among the glob results, plus that mtime.
func pickLatestBackupVerifyLog(paths []string) (string, time.Time, error) {
	var latest string
	var latestMtime time.Time
	for _, p := range paths {
		fi, err := backupVerifyStatFn(p)
		if err != nil {
			continue
		}
		if fi.ModTime().After(latestMtime) {
			latest = p
			latestMtime = fi.ModTime()
		}
	}
	if latest == "" {
		return "", time.Time{}, errors.New("no readable backup-verify-*.jsonl after stat sweep")
	}
	return latest, latestMtime, nil
}

// readLastJSONLLine returns the trimmed last non-empty line of body. Tolerates
// trailing newlines + blank lines per JSON Lines convention.
func readLastJSONLLine(body []byte) []byte {
	body = bytes.TrimRight(body, "\n")
	sc := bufio.NewScanner(bytes.NewReader(body))
	// Allow long lines for verbose Details payloads.
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	var last []byte
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) > 0 {
			last = append(last[:0], line...)
		}
	}
	return last
}

func newVaultBackupVerifyCmd() *cobra.Command {
	return &cobra.Command{
		Use:         "backup-verify",
		Short:       "Verify backup integrity from Phase 21c HARD-07 cron log",
		Long:        "Reads the most recent _tooling/logs/backup-verify-{ISO-week}.jsonl entry and exits 0 (green) / 1 (rot) / 4 (missing or stale). Graceful fallback when Phase 21c not yet shipped (exits 4 with diagnostic per CONTEXT D-26).",
		Annotations: vaultAnnotation,
		Args:        cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cmd.SilenceUsage = true
			_ = context.Background() // ctx unused; pure filesystem leaf
			logDir, err := resolveBackupVerifyLogDir()
			if err != nil {
				return &cliErrorWithExit{code: 4, msg: fmt.Sprintf("backup-verify: cannot resolve log dir: %v", err)}
			}

			globPattern := filepath.Join(logDir, "backup-verify-*.jsonl")
			matches, err := backupVerifyGlobFn(globPattern)
			if err != nil {
				return &cliErrorWithExit{code: 4, msg: fmt.Sprintf("backup-verify: glob %s: %v", globPattern, err)}
			}
			if len(matches) == 0 {
				return &cliErrorWithExit{
					code: 4,
					msg: fmt.Sprintf(
						"backup-verify: no logs at %s — Phase 21c HARD-07 not yet shipped (cron writer) or not yet run this ISO week; see ROADMAP §Phase 21c",
						globPattern,
					),
				}
			}

			latest, mtime, err := pickLatestBackupVerifyLog(matches)
			if err != nil {
				return &cliErrorWithExit{code: 4, msg: fmt.Sprintf("backup-verify: %v", err)}
			}
			if age := time.Since(mtime); age > backupVerifyStaleAfter {
				return &cliErrorWithExit{
					code: 4,
					msg: fmt.Sprintf(
						"backup-verify: log %s is stale (mtime %s; age %s > %s) — Phase 21c cron may be broken",
						latest, mtime.UTC().Format(time.RFC3339), age.Round(time.Hour), backupVerifyStaleAfter,
					),
				}
			}

			body, err := backupVerifyReadFn(latest)
			if err != nil {
				if errors.Is(err, fs.ErrNotExist) {
					return &cliErrorWithExit{code: 4, msg: fmt.Sprintf("backup-verify: log vanished between stat and read: %s", latest)}
				}
				return &cliErrorWithExit{code: 4, msg: fmt.Sprintf("backup-verify: read %s: %v", latest, err)}
			}
			lastLine := readLastJSONLLine(body)
			if len(lastLine) == 0 {
				return &cliErrorWithExit{code: 4, msg: fmt.Sprintf("backup-verify: log %s has no entries", latest)}
			}
			var entry backupVerifyEntry
			if err := json.Unmarshal(lastLine, &entry); err != nil {
				return &cliErrorWithExit{code: 4, msg: fmt.Sprintf("backup-verify: malformed jsonl entry in %s: %v", latest, err)}
			}
			outcome := strings.ToLower(strings.TrimSpace(entry.Outcome))
			switch outcome {
			case "green", "ok", "success":
				fmt.Fprintf(cmd.OutOrStdout(), "backup-verify: %s @ %s — %s\n", outcome, entry.Timestamp, entry.Details)
				return nil
			default:
				fmt.Fprintf(cmd.ErrOrStderr(), "backup-verify ROT detected: outcome=%q ts=%q details=%q (source=%s)\n",
					entry.Outcome, entry.Timestamp, entry.Details, latest)
				return &cliErrorWithExit{
					code: 1,
					msg:  fmt.Sprintf("backup-verify: outcome=%q (not green); see stderr for details", entry.Outcome),
				}
			}
		},
	}
}
