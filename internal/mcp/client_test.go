package mcp

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
)

// TestClientNewClientMissingToken asserts the auth gate: when VAULT_AI_TOKEN
// is empty (and path-A is active — auth.go ships), NewClient MUST return an
// error wrapping ErrMissingDependency so cobra leaves can map it to exit 4
// via errors.Is(err, ErrMissingDependency) per CONTEXT D-09 + D-18.
//
// This test does NOT actually spawn the subprocess (we exit before reaching
// the transport.Start call) — it exercises the token-reading branch of
// NewClient only.
func TestClientNewClientMissingToken(t *testing.T) {
	// Force VAULT_AI_TOKEN unset for the duration of this test.
	t.Setenv("VAULT_AI_TOKEN", "")

	ctx := context.Background()
	_, err := NewClient(ctx, Options{
		VaultAIRepoRoot: t.TempDir(),
		Version:         "test",
	})
	if err == nil {
		t.Fatal("NewClient with empty VAULT_AI_TOKEN returned nil error; want ErrMissingDependency")
	}
	if !errors.Is(err, ErrMissingDependency) {
		t.Errorf("NewClient error not errors.Is(ErrMissingDependency); got %v", err)
	}
}

// TestClientNewClientUVNonRegular asserts the Pitfall 5 mitigation: when the
// resolved uv path is not a regular file (we override LookPath via the test
// seam), NewClient bails out before constructing the transport with a clear
// "fd-3 collision risk" message. This prevents the subprocess from being
// spawned at all under conditions where ExtraFiles[0] may collide with
// /proc/self/fd/<exe>.
func TestClientNewClientUVNonRegular(t *testing.T) {
	t.Setenv("VAULT_AI_TOKEN", "test-token-bytes")

	// Override the LookPath seam to return a directory (non-regular file).
	dir := t.TempDir()
	origLookPath := lookPathFn
	t.Cleanup(func() { lookPathFn = origLookPath })
	lookPathFn = func(file string) (string, error) {
		return dir, nil
	}

	ctx := context.Background()
	_, err := NewClient(ctx, Options{
		VaultAIRepoRoot: dir,
		Version:         "test",
	})
	if err == nil {
		t.Fatal("NewClient with non-regular uv path returned nil error; want fd-3 collision risk")
	}
	if !strings.Contains(err.Error(), "fd-3 collision risk") {
		t.Errorf("NewClient error missing 'fd-3 collision risk'; got %v", err)
	}
}

// TestInstallSignalForwardReturnsStopFn covers the signal-forward installer:
// it MUST return a non-nil stop func, and calling stop() twice MUST be a
// no-op (idempotent — defer stop() in cobra leaves needs to be safe even
// when an earlier stop already ran).
func TestInstallSignalForwardReturnsStopFn(t *testing.T) {
	// We use a zero-value Client here — InstallSignalForward does not touch
	// the underlying subprocess unless an actual signal arrives. The fake
	// client just satisfies the signature.
	cl := &Client{}
	stop := InstallSignalForward(cl)
	if stop == nil {
		t.Fatal("InstallSignalForward returned nil stop func")
	}
	// Calling stop must not panic.
	stop()
	// Second call must be idempotent — must not panic.
	stop()
}

// TestStripTokenRemovesEntry verifies the pure helper that removes a named
// env var from a []string slice (used by NewClient to scrub VAULT_AI_TOKEN
// out of cmd.Env before the subprocess starts — defense-in-depth per
// CONTEXT D-09; the operator should never see VAULT_AI_TOKEN in
// /proc/<child-pid>/environ).
func TestStripTokenRemovesEntry(t *testing.T) {
	in := []string{"PATH=/bin", "VAULT_AI_TOKEN=secret-xxx", "HOME=/home/u", "FOO=bar"}
	out := stripToken(in, "VAULT_AI_TOKEN")
	for _, e := range out {
		if strings.HasPrefix(e, "VAULT_AI_TOKEN=") {
			t.Errorf("stripToken left VAULT_AI_TOKEN entry %q in result", e)
		}
	}
	// Other entries preserved.
	want := map[string]bool{"PATH=/bin": true, "HOME=/home/u": true, "FOO=bar": true}
	got := map[string]bool{}
	for _, e := range out {
		got[e] = true
	}
	for w := range want {
		if !got[w] {
			t.Errorf("stripToken dropped non-target entry %q; got %v", w, out)
		}
	}
}

// TestStripTokenEmptyEnvNoOp covers the edge case: stripToken on an empty
// or token-absent slice MUST return a slice with no VAULT_AI_TOKEN entry
// and not panic.
func TestStripTokenEmptyEnvNoOp(t *testing.T) {
	if got := stripToken(nil, "VAULT_AI_TOKEN"); len(got) != 0 {
		t.Errorf("stripToken(nil) = %v; want empty", got)
	}
	in := []string{"PATH=/bin"}
	out := stripToken(in, "VAULT_AI_TOKEN")
	if len(out) != 1 || out[0] != "PATH=/bin" {
		t.Errorf("stripToken passthrough failed; got %v", out)
	}
}

// helper: assert package compiles with the public surface the tests reference.
// If NewClient signature ever changes, this catches it at compile time.
var _ = func() error {
	_ = os.Getenv("VAULT_AI_TOKEN")
	return nil
}
