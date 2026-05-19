package mcp

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"
)

// TestMapErrorCodeToExitCode is the table-driven verification of the 8-code
// exit-code mapping per CONTEXT D-18 + vault-commands.md v1.3.0. Unknown codes
// MUST return 1 AND emit a stderr warning naming XREPO-01 + `ws vault doctor`
// so operators can react to drift between vault-ai's contract and the Go
// consumer.
func TestMapErrorCodeToExitCode(t *testing.T) {
	cases := []struct {
		name     string
		code     string
		wantExit int
	}{
		{name: "empty code returns 0 (success)", code: "", wantExit: 0},
		{name: "VALIDATION_FAILED returns 1", code: "VALIDATION_FAILED", wantExit: 1},
		{name: "BUDGET_EXCEEDED returns 2", code: "BUDGET_EXCEEDED", wantExit: 2},
		{name: "VISIBILITY_LEAK returns 3", code: "VISIBILITY_LEAK", wantExit: 3},
		{name: "MISSING_DEPENDENCY returns 4", code: "MISSING_DEPENDENCY", wantExit: 4},
		{name: "AUTH_FAILED returns 4", code: "AUTH_FAILED", wantExit: 4},
		{name: "RATE_LIMITED returns 4", code: "RATE_LIMITED", wantExit: 4},
		{name: "NOT_IMPLEMENTED returns 5", code: "NOT_IMPLEMENTED", wantExit: 5},
		{name: "DEDUP_BLOCKED returns 6", code: "DEDUP_BLOCKED", wantExit: 6},
		{name: "AGENT_TOOL_NOT_BOUND returns 7", code: "AGENT_TOOL_NOT_BOUND", wantExit: 7},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := MapErrorCodeToExitCode(tc.code)
			if got != tc.wantExit {
				t.Fatalf("MapErrorCodeToExitCode(%q) = %d; want %d", tc.code, got, tc.wantExit)
			}
		})
	}
}

// TestMapErrorCodeToExitCodeUnknownEmitsWarning captures stderr to verify that
// unknown codes (XREPO-01 drift signal) both return exit 1 AND emit a stderr
// warning containing "unknown error code" + "ws vault doctor" so the operator
// can run the diagnostic on the spot.
func TestMapErrorCodeToExitCodeUnknownEmitsWarning(t *testing.T) {
	origStderr := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stderr = w
	t.Cleanup(func() {
		os.Stderr = origStderr
	})

	got := MapErrorCodeToExitCode("MYSTERY_NEW_CODE")
	_ = w.Close()
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, r); err != nil {
		t.Fatalf("read captured stderr: %v", err)
	}

	if got != 1 {
		t.Errorf("unknown code exit code = %d; want 1", got)
	}
	captured := buf.String()
	if !strings.Contains(captured, "unknown error code") {
		t.Errorf("stderr missing 'unknown error code' substring; got %q", captured)
	}
	if !strings.Contains(captured, "ws vault doctor") {
		t.Errorf("stderr missing 'ws vault doctor' remediation hint; got %q", captured)
	}
	if !strings.Contains(captured, "MYSTERY_NEW_CODE") {
		t.Errorf("stderr missing offending code; got %q", captured)
	}
}

// TestEnvelopeExitCode covers the success and DEDUP_BLOCKED paths of
// (*Envelope).ExitCode — the convenience used by cobra leaves' RunE to convert
// a decoded envelope into a shell exit code.
func TestEnvelopeExitCode(t *testing.T) {
	cases := []struct {
		name string
		env  *Envelope
		want int
	}{
		{
			name: "success envelope returns 0",
			env:  &Envelope{OK: true},
			want: 0,
		},
		{
			name: "DEDUP_BLOCKED envelope returns 6",
			env:  &Envelope{OK: false, Error: &EnvelopeError{Code: "DEDUP_BLOCKED"}},
			want: 6,
		},
		{
			name: "VALIDATION_FAILED envelope returns 1",
			env:  &Envelope{OK: false, Error: &EnvelopeError{Code: "VALIDATION_FAILED"}},
			want: 1,
		},
		{
			name: "nil envelope returns 1 (defensive)",
			env:  nil,
			want: 1,
		},
		{
			name: "ok=false without Error returns 1 (malformed envelope)",
			env:  &Envelope{OK: false},
			want: 1,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.env.ExitCode()
			if got != tc.want {
				t.Fatalf("ExitCode() = %d; want %d", got, tc.want)
			}
		})
	}
}
