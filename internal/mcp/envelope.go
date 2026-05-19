// Package mcp wires workspace-cli `ws vault` subcommands to the vault-ai stdio
// MCP server. The package is the single subprocess-lifecycle chokepoint per
// CONTEXT D-05 (Phase 18): all 10 vault leaves route through NewClient/Call/Close
// so process-group teardown, signal forwarding, and fd-3 token transport live in
// one place instead of being duplicated per leaf.
//
// Envelope decoding + exit-code mapping live in this file. Subprocess lifecycle
// lives in client.go. fd-3 token transport lives in auth.go (path-A per
// CONTEXT D-09a). Generated tool types land in types.go (Plan 18-02).
package mcp

import (
	"encoding/json"
	"fmt"
	"os"
)

// Envelope is the MCP tool response wire shape per CONTEXT D-18 + vault-commands.md
// v1.3.0. Either OK==true and Data carries the tool payload, or OK==false and
// Error carries a structured failure.
type Envelope struct {
	OK    bool            `json:"ok"`
	Data  json.RawMessage `json:"data,omitempty"`
	Error *EnvelopeError  `json:"error,omitempty"`
}

// EnvelopeError carries the structured failure surface — Code maps to a Unix
// exit code via MapErrorCodeToExitCode; Message is operator-facing; Details
// carries tool-specific context (e.g. dedup similarity, existing_id, retry-after).
type EnvelopeError struct {
	Code    string          `json:"code"`
	Message string          `json:"message"`
	Details json.RawMessage `json:"details,omitempty"`
}

// MapErrorCodeToExitCode maps the 8 documented MCP error codes (per
// workspace-cli/docs/vault-commands.md v1.3.0 + CONTEXT D-18) onto Unix exit
// codes 0-7. Unknown codes return 1 and emit a stderr warning so the operator
// notices likely XREPO-01 contract drift (vault-ai contract bumped a new code
// the Go consumer does not yet know about).
//
// Exit code table (locked by ADR-int-03 §Exit codes; cannot reshape without
// supersession ADR):
//
//	0 = success (empty code)
//	1 = VALIDATION_FAILED
//	2 = BUDGET_EXCEEDED
//	3 = VISIBILITY_LEAK
//	4 = MISSING_DEPENDENCY | AUTH_FAILED | RATE_LIMITED (auth/availability)
//	5 = NOT_IMPLEMENTED
//	6 = DEDUP_BLOCKED (Phase 17)
//	7 = AGENT_TOOL_NOT_BOUND (Phase 16)
//	8-127 reserved
func MapErrorCodeToExitCode(code string) int {
	switch code {
	case "":
		return 0
	case "VALIDATION_FAILED":
		return 1
	case "BUDGET_EXCEEDED":
		return 2
	case "VISIBILITY_LEAK":
		return 3
	case "MISSING_DEPENDENCY", "AUTH_FAILED", "RATE_LIMITED":
		return 4
	case "NOT_IMPLEMENTED":
		return 5
	case "DEDUP_BLOCKED":
		return 6
	case "AGENT_TOOL_NOT_BOUND":
		return 7
	default:
		fmt.Fprintf(os.Stderr, "warning: unknown error code %q from MCP — possibly XREPO-01 drift; run `ws vault doctor`\n", code)
		return 1
	}
}

// ExitCode is a convenience for cobra leaves' RunE: success (OK && Error == nil)
// maps to 0; otherwise the Error.Code drives the exit code via MapErrorCodeToExitCode.
// A nil-Error failure (OK == false with no Error block) is treated as a generic
// validation failure (exit 1) — this protects against malformed envelopes that
// claim failure without naming a code.
func (e *Envelope) ExitCode() int {
	if e == nil {
		return 1
	}
	if e.OK && e.Error == nil {
		return MapErrorCodeToExitCode("")
	}
	if e.Error == nil {
		return MapErrorCodeToExitCode("VALIDATION_FAILED")
	}
	return MapErrorCodeToExitCode(e.Error.Code)
}
