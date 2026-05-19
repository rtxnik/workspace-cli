package cmd

// vault_doctor.go — `ws vault doctor` leaf (CLI-10).
//
// 10th and final leaf per CONTEXT D-01. Surfaces 5 read-only diagnostic
// checks per CONTEXT D-12 + CLI-10:
//
//   1. orphan-mcp-subprocess  — pgrep -fc vault_ai/adapter_stdio/server.py
//   2. stale-lock-files       — find ~/projects/vault-ai/_tooling/state/ -name '*.lock' -mmin +60
//   3. vault-ai-token         — os.Getenv("VAULT_AI_TOKEN") presence
//   4. token-fd-pass          — dry-run mcp.NewClient + ListTools + Close
//   5. xrepo-contract-parity  — exec bash check-xrepo-contract.sh; exit code
//
// READ-ONLY BY DEFAULT (CONTEXT D-13 + memory feedback_no_auto_state_mutation
// + Phase 22 D-09/D-10 manual-recovery lineage):
//
//   - Default invocation NEVER mutates state — only reports + remediation hints.
//   - --kill-orphans gates mutation #1 (kill detected orphan PIDs).
//   - --clear-stale-locks gates mutation #2 (remove stale lock files).
//   - BOTH require --yes OR an interactive output.Confirm acceptance before
//     the actual kill/remove fires.
//
// Mutation discipline is structurally enforced by TestVaultDoctorReadOnlyByDefault
// which mocks the kill/remove fns and asserts call-count = 0 when no flags
// are passed, even with orphans + stale locks present in the mocked state.
//
// Exit codes (worst-band aggregator):
//   - 0 — all 5 green
//   - 1 — at least one yellow (advisory: stale locks, MCP yellow)
//   - 2 — at least one red (critical: orphans, missing token, fd-pass broken, xrepo drift)
//
// Output modes:
//   - default (human) — table with ●/⚠/✗ band prefix per check
//   - --json — NDJSON, one doctorCheck object per line

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/rtxnik/workspace-cli/internal/mcp"
	"github.com/rtxnik/workspace-cli/internal/output"
	"github.com/spf13/cobra"
)

// Band names reuse the statusBand type + bandGreen/bandYellow/bandRed
// constants from vault_status.go (CLI-01) so the vault leaves share one
// vocabulary for severity bands. JSON marshalling of statusBand renders to
// the same string values ("green"/"yellow"/"red") as the wire shape demands.

// doctorCheck is the single record emitted by every check function. JSON tags
// drive the --json NDJSON rendering; human-mode renderer reads the same fields.
type doctorCheck struct {
	Name        string     `json:"name"`
	Band        statusBand `json:"band"`
	Detail      string     `json:"detail,omitempty"`
	Remediation string     `json:"remediation,omitempty"`
	// PIDs and LockPaths carry the mutation-target data so --kill-orphans /
	// --clear-stale-locks know what to operate on without re-running the
	// check. Omitted from JSON when empty to keep the wire shape lean.
	PIDs      []int    `json:"pids,omitempty"`
	LockPaths []string `json:"lock_paths,omitempty"`
}

// Package-level seams for tests. Production wires to the *Impl functions
// below; unit tests overwrite these to inject mocked check results without
// spawning live subprocesses.
var (
	doctorOrphanCheckFn    = checkOrphanMCPImpl
	doctorStaleLockCheckFn = checkStaleLocksImpl
	doctorTokenCheckFn     = checkMissingTokenImpl
	doctorFDPassCheckFn    = checkBrokenFDPassImpl
	doctorXrepoCheckFn     = checkXrepoDriftImpl

	// Mutation seams. doctorKillFn receives a slice of PIDs to signal;
	// doctorRemoveFn receives a slice of file paths to remove. Tests assert
	// call counts on these to enforce the read-only-by-default discipline.
	doctorKillFn   = killOrphanProcessesImpl
	doctorRemoveFn = removeStaleLockFilesImpl

	// Confirm seam — same convention as vaultIngestConfirmFn so vault leaves
	// have a uniform mocking surface.
	doctorConfirmFn = output.Confirm
)

// --------------------------------------------------------------------------
// Check implementations
// --------------------------------------------------------------------------

// checkOrphanMCPImpl invokes `pgrep -fc vault_ai/adapter_stdio/server.py` and
// reports red when count > 0. Detail carries the count + the leaked PIDs.
func checkOrphanMCPImpl(_ context.Context) *doctorCheck {
	check := &doctorCheck{Name: "orphan-mcp-subprocess"}

	out, err := exec.Command("pgrep", "-fc", "vault_ai/adapter_stdio/server.py").Output()
	count := strings.TrimSpace(string(out))
	// pgrep -fc exits non-zero (status 1) when no matches found and prints "0";
	// treat that as the green path. Distinguish "no matches" from a real
	// pgrep error by checking whether the output is parseable as int.
	if count == "" || count == "0" {
		check.Band = bandGreen
		check.Detail = "0 orphan MCP subprocesses"
		return check
	}
	n, perr := strconv.Atoi(count)
	if perr != nil {
		// pgrep failed structurally (not "no matches"). Surface as yellow
		// since we cannot positively assert green.
		check.Band = bandYellow
		check.Detail = fmt.Sprintf("pgrep returned non-numeric output %q (err=%v)", count, err)
		check.Remediation = "verify pgrep is installed + on PATH"
		return check
	}
	if n == 0 {
		check.Band = bandGreen
		check.Detail = "0 orphan MCP subprocesses"
		return check
	}

	// Resolve PIDs for the remediation path. Use `pgrep -f` (no -c) to get
	// one PID per line.
	pidsOut, _ := exec.Command("pgrep", "-f", "vault_ai/adapter_stdio/server.py").Output()
	var pids []int
	for _, line := range strings.Split(strings.TrimSpace(string(pidsOut)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if pid, perr := strconv.Atoi(line); perr == nil {
			pids = append(pids, pid)
		}
	}

	check.Band = bandRed
	check.Detail = fmt.Sprintf("%d orphan MCP subprocesses detected (PIDs: %v)", n, pids)
	check.Remediation = "re-run with `ws vault doctor --kill-orphans --yes` to clean up (operator-controlled per CONTEXT D-13)"
	check.PIDs = pids
	return check
}

// checkStaleLocksImpl walks ~/projects/vault-ai/_tooling/state/ for *.lock
// files older than 60min. Returns green if directory absent or no stale locks.
// CONTEXT D-12 §2: yellow band; locks older than 60min are suspect (Phase 14
// audit-chain lock pattern reference).
func checkStaleLocksImpl(_ context.Context) *doctorCheck {
	check := &doctorCheck{Name: "stale-lock-files"}

	home, err := os.UserHomeDir()
	if err != nil {
		check.Band = bandYellow
		check.Detail = fmt.Sprintf("cannot resolve home dir: %v", err)
		return check
	}
	stateDir := filepath.Join(home, "projects", "vault-ai", "_tooling", "state")
	info, err := os.Stat(stateDir)
	if err != nil || !info.IsDir() {
		// Per verify-before-claim: if state dir absent, green + diagnostic
		// (Phase 14 audit-chain layout not yet present).
		check.Band = bandGreen
		check.Detail = fmt.Sprintf("no _tooling/state/ directory at %s (Phase 14 audit-chain layout not yet present)", stateDir)
		return check
	}

	var stalePaths []string
	staleThreshold := 60 * time.Minute
	now := time.Now()
	walkErr := filepath.Walk(stateDir, func(path string, info os.FileInfo, werr error) error {
		if werr != nil || info == nil {
			return nil // tolerate per-file errors; don't fail the whole walk
		}
		if info.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".lock") {
			return nil
		}
		if now.Sub(info.ModTime()) > staleThreshold {
			stalePaths = append(stalePaths, path)
		}
		return nil
	})
	if walkErr != nil {
		check.Band = bandYellow
		check.Detail = fmt.Sprintf("walk %s: %v", stateDir, walkErr)
		return check
	}

	if len(stalePaths) == 0 {
		check.Band = bandGreen
		check.Detail = "no stale lock files"
		return check
	}
	check.Band = bandYellow
	check.Detail = fmt.Sprintf("%d stale lock(s): %s", len(stalePaths), strings.Join(stalePaths, ", "))
	check.Remediation = "re-run with `ws vault doctor --clear-stale-locks --yes` to remove (operator-controlled per CONTEXT D-13)"
	check.LockPaths = stalePaths
	return check
}

// checkMissingTokenImpl reports red when VAULT_AI_TOKEN is unset or empty.
// Never logs the actual token value — only presence/absence.
func checkMissingTokenImpl() *doctorCheck {
	check := &doctorCheck{Name: "vault-ai-token"}
	if os.Getenv("VAULT_AI_TOKEN") == "" {
		check.Band = bandRed
		check.Detail = "VAULT_AI_TOKEN unset or empty"
		check.Remediation = "provision via chezmoi+age per ADR-ai-06 §Auth; see dotfiles ADR-sec-02 for the age key flow"
		return check
	}
	check.Band = bandGreen
	check.Detail = "VAULT_AI_TOKEN present (value redacted)"
	return check
}

// checkBrokenFDPassImpl dry-runs the full MCP handshake: NewClient → ListTools
// → Close. Each failure stage is identified in the detail (stage=newclient,
// stage=listtools, stage=close). Green only when all three succeed.
func checkBrokenFDPassImpl(ctx context.Context) *doctorCheck {
	check := &doctorCheck{Name: "token-fd-pass"}

	// Bound the dry-run separately so a hung subprocess doesn't eat the leaf's
	// own context budget.
	dryCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	cl, err := mcp.NewClient(dryCtx, mcp.Options{
		VaultAIRepoRoot: os.Getenv("VAULT_AI_REPO_ROOT"),
		Version:         "ws-vault-doctor",
	})
	if err != nil {
		check.Band = bandRed
		check.Detail = fmt.Sprintf("stage=newclient: %v", err)
		check.Remediation = "see RESEARCH §Pitfall 7 + Plan 18-01 for fd-3 wiring; check `ws vault doctor` token check above"
		return check
	}
	if _, err := cl.ListTools(dryCtx); err != nil {
		_ = cl.Close(dryCtx)
		check.Band = bandRed
		check.Detail = fmt.Sprintf("stage=listtools: %v", err)
		check.Remediation = "see RESEARCH §Pitfall 7 + Plan 18-01 for fd-3 wiring"
		return check
	}
	if err := cl.Close(dryCtx); err != nil {
		check.Band = bandRed
		check.Detail = fmt.Sprintf("stage=close: %v", err)
		check.Remediation = "see RESEARCH §Pitfall 4 + client.go process-group teardown"
		return check
	}

	check.Band = bandGreen
	check.Detail = "MCP handshake successful (NewClient + ListTools + Close)"
	return check
}

// checkXrepoDriftImpl invokes vault-ai/_tooling/lint/check-xrepo-contract.sh
// and reports red on non-zero exit. Detail carries the first line of the
// walker's stderr/stdout (full diff would be too verbose for table mode).
func checkXrepoDriftImpl(ctx context.Context) *doctorCheck {
	check := &doctorCheck{Name: "xrepo-contract-parity"}

	home, err := os.UserHomeDir()
	if err != nil {
		check.Band = bandYellow
		check.Detail = fmt.Sprintf("cannot resolve home dir: %v", err)
		return check
	}
	script := filepath.Join(home, "projects", "vault-ai", "_tooling", "lint", "check-xrepo-contract.sh")
	if _, serr := os.Stat(script); serr != nil {
		check.Band = bandYellow
		check.Detail = fmt.Sprintf("check-xrepo-contract.sh not found at %s: %v", script, serr)
		check.Remediation = "verify Phase 17 deliverable is present in vault-ai checkout"
		return check
	}

	cmd := exec.CommandContext(ctx, "bash", script)
	out, err := cmd.CombinedOutput()
	if err == nil {
		check.Band = bandGreen
		check.Detail = "no xrepo contract drift"
		return check
	}

	firstLine := strings.SplitN(strings.TrimSpace(string(out)), "\n", 2)[0]
	if firstLine == "" {
		firstLine = err.Error()
	}
	check.Band = bandRed
	check.Detail = fmt.Sprintf("check-xrepo-contract.sh exit: %s: %s", err.Error(), firstLine)
	check.Remediation = "run `uv run --project ~/projects/vault-ai/_tooling/mcp -- python -m vault_ai.tooling.check_xrepo --gen-types-check` to regenerate Go types; verify tools.json contract_version"
	return check
}

// --------------------------------------------------------------------------
// Mutation implementations (gated by --kill-orphans / --clear-stale-locks)
// --------------------------------------------------------------------------

// killOrphanProcessesImpl SIGTERMs each PID, then waits up to 5s before
// SIGKILL escalation. Mirrors client.go close grace period.
func killOrphanProcessesImpl(pids []int) error {
	var firstErr error
	for _, pid := range pids {
		if err := syscall.Kill(pid, syscall.SIGTERM); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("SIGTERM pid %d: %w", pid, err)
		}
	}
	// Brief grace, then SIGKILL any survivors.
	time.Sleep(5 * time.Second)
	for _, pid := range pids {
		// syscall.Kill(pid, 0) probes existence; signal 0 raises EINVAL if
		// the process is gone, no-op otherwise.
		if err := syscall.Kill(pid, 0); err == nil {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	}
	return firstErr
}

// removeStaleLockFilesImpl removes each lock file. Returns the first error
// encountered but continues attempting the rest (best-effort cleanup).
func removeStaleLockFilesImpl(paths []string) error {
	var firstErr error
	for _, p := range paths {
		if err := os.Remove(p); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("remove %s: %w", p, err)
		}
	}
	return firstErr
}

// --------------------------------------------------------------------------
// Aggregator + renderer + Cobra wiring
// --------------------------------------------------------------------------

// runChecks invokes all 5 check functions and returns the slice in display
// order (orphan, stale-lock, token, fd-pass, xrepo). Order matches CONTEXT
// D-12 numbering so operator vocabulary stays stable.
func runChecks(ctx context.Context) []*doctorCheck {
	return []*doctorCheck{
		doctorOrphanCheckFn(ctx),
		doctorStaleLockCheckFn(ctx),
		doctorTokenCheckFn(),
		doctorFDPassCheckFn(ctx),
		doctorXrepoCheckFn(ctx),
	}
}

// worstBand returns the maximum severity across the slice. Order:
// green < yellow < red.
func worstBand(checks []*doctorCheck) statusBand {
	worst := bandGreen
	for _, c := range checks {
		switch c.Band {
		case bandRed:
			return bandRed
		case bandYellow:
			worst = bandYellow
		}
	}
	return worst
}

// bandToExitCode maps the worst observed band to the CONTEXT D-12 aggregator
// exit code: green=0, yellow=1, red=2.
func bandToExitCode(band statusBand) int {
	switch band {
	case bandRed:
		return 2
	case bandYellow:
		return 1
	default:
		return 0
	}
}

// renderChecks writes the checks to w. JSON mode emits NDJSON (one
// doctorCheck per line). Human mode emits a table with ●/⚠/✗ prefix.
func renderChecks(w io.Writer, checks []*doctorCheck, jsonMode bool) error {
	if jsonMode {
		enc := json.NewEncoder(w)
		for _, c := range checks {
			if err := enc.Encode(c); err != nil {
				return fmt.Errorf("encode check %q: %w", c.Name, err)
			}
		}
		return nil
	}
	for _, c := range checks {
		var prefix string
		switch c.Band {
		case bandGreen:
			prefix = "✓"
		case bandYellow:
			prefix = "⚠"
		case bandRed:
			prefix = "✗"
		default:
			prefix = "?"
		}
		_, _ = fmt.Fprintf(w, "%s %-26s %s\n", prefix, c.Name, c.Detail)
		if c.Remediation != "" {
			_, _ = fmt.Fprintf(w, "  → %s\n", c.Remediation)
		}
	}
	return nil
}

// applyMutations executes the opt-in mutations gated by --kill-orphans and
// --clear-stale-locks. Each gate independently requires --yes OR Confirm
// acceptance before invoking the kill/remove fn.
//
// Returns silently when neither flag is set (the read-only-by-default path).
func applyMutations(cmd *cobra.Command, checks []*doctorCheck) {
	killOrphans, _ := cmd.Flags().GetBool("kill-orphans")
	clearLocks, _ := cmd.Flags().GetBool("clear-stale-locks")
	yes, _ := cmd.Flags().GetBool("yes")

	// Orphan-kill path.
	if killOrphans {
		var orphan *doctorCheck
		for _, c := range checks {
			if c.Name == "orphan-mcp-subprocess" {
				orphan = c
				break
			}
		}
		if orphan != nil && len(orphan.PIDs) > 0 {
			proceed := yes
			if !proceed {
				title := fmt.Sprintf("Kill %d orphan MCP subprocess(es)?", len(orphan.PIDs))
				desc := fmt.Sprintf(
					"This will SIGTERM PIDs %v (5s grace then SIGKILL). Falsely killing a legitimate session is destructive per Phase 22 D-10 manual-recovery discipline.",
					orphan.PIDs,
				)
				proceed = doctorConfirmFn(title, desc)
			}
			if proceed {
				if err := doctorKillFn(orphan.PIDs); err != nil {
					output.Warn(fmt.Sprintf("kill-orphans: %v", err))
				} else {
					output.Success(fmt.Sprintf("Killed %d orphan MCP subprocess(es)", len(orphan.PIDs)))
				}
			} else {
				output.Info("Aborted --kill-orphans (operator declined or Confirm returned false)")
			}
		}
	}

	// Stale-lock-clear path.
	if clearLocks {
		var stale *doctorCheck
		for _, c := range checks {
			if c.Name == "stale-lock-files" {
				stale = c
				break
			}
		}
		if stale != nil && len(stale.LockPaths) > 0 {
			proceed := yes
			if !proceed {
				title := fmt.Sprintf("Remove %d stale lock file(s)?", len(stale.LockPaths))
				desc := fmt.Sprintf("Paths: %v. Removing a legitimate active lock could corrupt audit chain state per Phase 14 lock-file invariants.", stale.LockPaths)
				proceed = doctorConfirmFn(title, desc)
			}
			if proceed {
				if err := doctorRemoveFn(stale.LockPaths); err != nil {
					output.Warn(fmt.Sprintf("clear-stale-locks: %v", err))
				} else {
					output.Success(fmt.Sprintf("Removed %d stale lock file(s)", len(stale.LockPaths)))
				}
			} else {
				output.Info("Aborted --clear-stale-locks (operator declined or Confirm returned false)")
			}
		}
	}
}

func newVaultDoctorCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:         "doctor",
		Short:       "Diagnose MCP subprocess + token + xrepo contract health (read-only by default)",
		Long: "Runs 5 read-only diagnostic checks per CONTEXT D-12: orphan MCP subprocesses, " +
			"stale lock files, VAULT_AI_TOKEN presence, MCP fd-pass handshake, xrepo contract parity. " +
			"Exit 0 (all green) / 1 (≥1 yellow) / 2 (≥1 red). " +
			"Mutation is opt-in only via --kill-orphans + --clear-stale-locks (each gated by --yes or " +
			"output.Confirm) per memory feedback_no_auto_state_mutation + Phase 22 D-09/D-10 lineage.",
		Annotations: vaultAnnotation,
		Args:        cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cmd.SilenceUsage = true
			ctx := cmd.Context()
			if ctx == nil {
				ctx = context.Background()
			}

			checks := runChecks(ctx)
			jsonFlag, _ := cmd.Flags().GetBool("json")
			if err := renderChecks(cmd.OutOrStdout(), checks, jsonFlag); err != nil {
				return fmt.Errorf("doctor: render: %w", err)
			}

			// Apply opt-in mutations AFTER rendering so the operator sees the
			// detected state before any mutation fires.
			applyMutations(cmd, checks)

			band := worstBand(checks)
			code := bandToExitCode(band)
			if code == 0 {
				return nil
			}
			return &cliErrorWithExit{
				code: code,
				msg:  fmt.Sprintf("doctor: worst band=%s (see %d check(s) above)", band, len(checks)),
			}
		},
	}
	cmd.Flags().Bool("kill-orphans", false, "Kill detected orphan MCP subprocesses (opt-in mutation; gated by --yes / Confirm per CONTEXT D-13)")
	cmd.Flags().Bool("clear-stale-locks", false, "Remove stale lock files (opt-in mutation; gated by --yes / Confirm per CONTEXT D-13)")
	cmd.Flags().Bool("yes", false, "Skip confirmation prompts for --kill-orphans / --clear-stale-locks (Phase 22 D-09/D-10 discipline)")
	return cmd
}
