package mcp

// auth.go implements the path-A fd-3 token transport per CONTEXT D-09 + D-09a
// (Phase 18 OQ-2 operator decision, 2026-05-18). The vault-ai stdio adapter
// reads VAULT_AI_TOKEN from file descriptor 3 instead of an environment
// variable so the token bytes never appear in /proc/<pid>/environ or `ps -e e`
// surface. This is a defense-in-depth measure layered on top of stdio's
// process-ownership trust boundary (ADR-ai-06).
//
// Path A ships unconditionally per Plan 18-01 Task 2a:
//   - Go parent: NewTokenPipe creates an os.Pipe; a goroutine writes the
//     token bytes onto the writer end and closes it so the reader EOFs cleanly.
//   - client.go wires the reader end into cmd.ExtraFiles[0] so the child sees
//     fd 3 (stdin/stdout/stderr occupy 0/1/2).
//   - VAULT_AI_TOKEN is ALSO stripped from cmd.Env (defense in depth).
//   - The vault-ai adapter_stdio bootstrap reads os.fdopen(3) (Task 2b
//     vault-ai-side modification).

import (
	"errors"
	"fmt"
	"os"
)

// ErrMissingDependency is the package-level sentinel returned (wrapped) by
// NewClient when a required runtime dependency is absent — most commonly
// VAULT_AI_TOKEN unset in the operator's shell environment. Cobra leaves
// can errors.Is(err, ErrMissingDependency) and surface exit 4 per CONTEXT
// D-18 + workspace-cli/docs/vault-commands.md v1.3.0.
var ErrMissingDependency = errors.New("missing dependency")

// errMissingDepWrap wraps a context message around ErrMissingDependency so
// callers see actionable text while still being able to errors.Is() against
// the sentinel. Internal helper; not exported.
func errMissingDepWrap(msg string) error {
	return fmt.Errorf("%s: %w", msg, ErrMissingDependency)
}

// NewTokenPipe creates an os.Pipe(), writes the token bytes onto the writer
// end in a background goroutine, closes the writer when the write completes,
// and returns the reader end for the caller to hand to cmd.ExtraFiles[0].
//
// The goroutine pattern ensures the parent does not block on a kernel pipe-
// buffer-full condition for unusually long tokens — the child can read at its
// own pace and EOF naturally once we close the writer.
//
// Errors from os.Pipe() are returned immediately. Write errors inside the
// goroutine are intentionally ignored — the worst-case observable failure is
// the child reading a truncated token, which surfaces as an AUTH_FAILED
// envelope from the adapter (exit 4 via MapErrorCodeToExitCode).
func NewTokenPipe(token string) (*os.File, error) {
	tokenR, tokenW, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("os.Pipe for VAULT_AI_TOKEN fd-3 transport: %w", err)
	}
	go func() {
		defer tokenW.Close()
		_, _ = tokenW.Write([]byte(token))
	}()
	return tokenR, nil
}

// CheckUVPath mitigates Pitfall 5 (golang/go #66654): when ExtraFiles[0] is
// set and the resolved binary lives at a non-regular path (symlink target
// swapped during exec, a directory, a device node), the Go runtime's
// /proc/self/fd/<exe> mechanism can collide with fd 3 and cause undefined
// behavior. We stat() the path up-front and reject anything that is not a
// regular file, citing the "fd-3 collision risk" string in the error message
// so tests and operators can grep for it.
//
// The check is cheap (one syscall) and runs once per NewClient invocation.
func CheckUVPath(uvPath string) error {
	info, err := os.Stat(uvPath)
	if err != nil {
		return fmt.Errorf("uv binary at %q: %w (fd-3 collision risk if path unstable)", uvPath, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("uv at non-regular path %q (mode=%s) — fd-3 collision risk per golang/go #66654", uvPath, info.Mode())
	}
	return nil
}
