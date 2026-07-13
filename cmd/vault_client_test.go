package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/rtxnik/workspace-cli/internal/mcp"
	"github.com/spf13/cobra"
)

func TestVaultErrExitMapsCodeAndMessage(t *testing.T) {
	err := vaultErrExit("ingest", &mcp.EnvelopeError{Code: "DEDUP_BLOCKED", Message: "too similar"})
	var cerr *cliErrorWithExit
	if !errors.As(err, &cerr) {
		t.Fatalf("expected *cliErrorWithExit; got %T", err)
	}
	if cerr.code != 6 { // DEDUP_BLOCKED -> 6
		t.Errorf("expected exit 6; got %d", cerr.code)
	}
	if cerr.msg != "ingest: DEDUP_BLOCKED: too similar" {
		t.Errorf("unexpected msg %q", cerr.msg)
	}
}

func newRenderCmd(jsonMode bool, out *bytes.Buffer) *cobra.Command {
	c := &cobra.Command{Use: "x"}
	c.Flags().Bool("json", jsonMode, "")
	c.SetOut(out)
	return c
}

func TestVaultRenderResultTransportError(t *testing.T) {
	err := vaultRenderResult(newRenderCmd(false, &bytes.Buffer{}), "search", nil, errors.New("boom"))
	if err == nil || !strings.Contains(err.Error(), "search: boom") {
		t.Fatalf("expected wrapped transport error; got %v", err)
	}
}

func TestVaultRenderResultNilEnvelope(t *testing.T) {
	err := vaultRenderResult(newRenderCmd(false, &bytes.Buffer{}), "search", nil, nil)
	if err == nil || err.Error() != "search: nil envelope" {
		t.Fatalf("expected nil-envelope error; got %v", err)
	}
}

func TestVaultRenderResultEnvelopeError(t *testing.T) {
	env := &mcp.Envelope{OK: false, Error: &mcp.EnvelopeError{Code: "VALIDATION_FAILED", Message: "bad"}}
	err := vaultRenderResult(newRenderCmd(false, &bytes.Buffer{}), "validate", env, nil)
	var cerr *cliErrorWithExit
	if !errors.As(err, &cerr) || cerr.code != 1 {
		t.Fatalf("expected *cliErrorWithExit code 1; got %v", err)
	}
}

func TestVaultRenderResultSuccessRenders(t *testing.T) {
	var out bytes.Buffer
	env := &mcp.Envelope{OK: true, Data: json.RawMessage(`{"id":"foo"}`)}
	if err := vaultRenderResult(newRenderCmd(true, &out), "search", env, nil); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if !strings.Contains(out.String(), `"id"`) {
		t.Errorf("expected rendered data; got %q", out.String())
	}
}

// TestSignalContext_ParentCancelPropagates: cancelling the parent cancels the
// derived ctx (the MCP call's parent context is honored).
func TestSignalContext_ParentCancelPropagates(t *testing.T) {
	parent, cancelParent := context.WithCancel(context.Background())
	ctx, stop := signalContext(parent)
	defer stop()

	cancelParent()
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("derived ctx did not cancel when parent was cancelled")
	}
}

// TestSignalContext_StopDetaches: the stop func cancels the ctx and detaches
// the handler (deferred by callers on RunE return).
func TestSignalContext_StopDetaches(t *testing.T) {
	ctx, stop := signalContext(context.Background())
	stop()
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("ctx not cancelled after stop()")
	}
	if !errors.Is(ctx.Err(), context.Canceled) {
		t.Errorf("ctx.Err() = %v, want context.Canceled", ctx.Err())
	}
}

// TestSignalContext_CancelsOnSignal proves the interruptibility mechanism: a
// SIGTERM delivered to this process cancels the derived ctx. NotifyContext
// diverts the signal, so the test binary is NOT terminated. Must not run in
// parallel (process-wide signal).
func TestSignalContext_CancelsOnSignal(t *testing.T) {
	ctx, stop := signalContext(context.Background())
	defer stop()

	if err := syscall.Kill(syscall.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatalf("kill self: %v", err)
	}
	select {
	case <-ctx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("ctx not cancelled after SIGTERM — signal wiring broken")
	}
}
