// Package procx owns the "run a command under a hard deadline and capture its
// output" primitive shared across the codebase. It lives in its own leaf
// package (no internal imports) so any consumer — docker, workspace, cmd —
// can use the same bounded-exec discipline without import cycles (mirrors the
// internal/fsutil leaf precedent).
//
// Deliberately NOT provided here: streaming/interactive shell-outs
// (devpod ssh/logs/up, docker build, reindex — they wire Stdout/Stderr to the
// terminal), the persistent MCP subprocess (internal/mcp), and any unbounded
// mode — a timeout is always required; the streaming callers own their
// unbounded-by-design paths.
package procx

import (
	"context"
	"os/exec"
	"time"
)

// Run executes name+args under a hard deadline and returns stdout only
// (stdlib Output semantics: on failure, captured stderr arrives in
// *exec.ExitError.Stderr). The effective deadline is the earlier of the
// caller ctx's own deadline and now+timeout, so a wedged binary can hang
// neither this call nor the caller's larger operation.
//
// Errors are returned UNWRAPPED so callers keep stdlib error semantics:
// errors.Is(err, exec.ErrNotFound), errors.As(&exec.ExitError), and the
// exact error text end users already see.
func Run(ctx context.Context, timeout time.Duration, name string, args ...string) ([]byte, error) {
	tctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return exec.CommandContext(tctx, name, args...).Output()
}

// RunCombined executes name+args under the same deadline discipline as Run
// and returns interleaved stdout+stderr (stdlib CombinedOutput semantics) —
// for callers that surface merged diagnostics, e.g. docker exec wrappers.
// Errors are returned unwrapped, exactly as Run.
func RunCombined(ctx context.Context, timeout time.Duration, name string, args ...string) ([]byte, error) {
	tctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return exec.CommandContext(tctx, name, args...).CombinedOutput()
}
