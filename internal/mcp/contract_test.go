package mcp

// contract_test.go implements TestContractVersionParity — the Go-side
// consumer surface of the XREPO-01 three-surface contract_version parity
// gate per CONTEXT D-16 + CLI-14. Complements vault-ai's
// _tooling/lint/check-xrepo-contract.sh bash walker (Phase 17) by adding a
// fourth invocation surface: Go test asserting drift even when the walker
// is bypassed.
//
// Three surfaces are read at test time and compared byte-identical:
//   1. vault-ai/_tooling/mcp/contract/tools.json — top-level contract_version
//   2. workspace-cli/docs/vault-commands.md — frontmatter mcp_contract_version
//   3. workflow-kit/.claude/settings.local.json — mcp.servers.vault-ai.contract_version
//
// Surfaces 1+2 are mandatory; surface 3 (workflow-kit) is soft-skipped if
// the file is absent (not all operator environments have workflow-kit
// checked out alongside).

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// workspaceRootForTest returns the absolute path to ~/projects (or whatever
// $WORKSPACE_ROOT overrides to). All three surface files are resolved
// relative to this root. Tests run from workspace-cli/internal/mcp so the
// default is two parent directories up from PWD plus a ~/projects shortcut.
func workspaceRootForTest(t *testing.T) string {
	t.Helper()
	if v := os.Getenv("WORKSPACE_ROOT"); v != "" {
		return v
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("UserHomeDir: %v", err)
	}
	return filepath.Join(home, "projects")
}

// readVaultAITools opens vault-ai/_tooling/mcp/contract/tools.json,
// json.Unmarshal-extracts the top-level contract_version, and returns it.
// File missing or malformed = error (this is a mandatory surface).
func readVaultAITools(root string) (string, error) {
	path := filepath.Join(root, "vault-ai", "_tooling", "mcp", "contract", "tools.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	var doc struct {
		ContractVersion string `json:"contract_version"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return "", fmt.Errorf("parse %s as JSON: %w", path, err)
	}
	if doc.ContractVersion == "" {
		return "", fmt.Errorf("%s has empty contract_version field", path)
	}
	return doc.ContractVersion, nil
}

// readWorkspaceCLIDoc reads workspace-cli/docs/vault-commands.md and extracts
// the frontmatter field `mcp_contract_version` via regex (matches the Python
// analog `_live_workspace_cli_version` in
// vault-ai/_tooling/mcp/tests/test_xrepo_drift.py L52-58).
func readWorkspaceCLIDoc(root string) (string, error) {
	path := filepath.Join(root, "workspace-cli", "docs", "vault-commands.md")
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	re := regexp.MustCompile(`(?m)^mcp_contract_version:\s*"([^"]+)"`)
	m := re.FindStringSubmatch(string(data))
	if len(m) < 2 {
		return "", fmt.Errorf("%s has no mcp_contract_version frontmatter field", path)
	}
	return m[1], nil
}

// readWorkflowKitSettings opens workflow-kit/.claude/settings.local.json and
// extracts mcp.servers."vault-ai".contract_version. Returns empty string + nil
// when the file is absent (soft-skip signal per CONTEXT D-16; not all
// operator environments have workflow-kit checked out).
func readWorkflowKitSettings(root string) (string, error) {
	path := filepath.Join(root, "workflow-kit", ".claude", "settings.local.json")
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", nil // soft-skip
		}
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	// Nested structure: { "mcp": { "servers": { "vault-ai": { "contract_version": "X.Y.Z" } } } }.
	// We use generic map[string]any to tolerate other top-level keys.
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		return "", fmt.Errorf("parse %s as JSON: %w", path, err)
	}
	mcp, _ := doc["mcp"].(map[string]any)
	if mcp == nil {
		return "", nil // workflow-kit may exist without MCP config; soft-skip
	}
	servers, _ := mcp["servers"].(map[string]any)
	if servers == nil {
		return "", nil
	}
	vaultAI, _ := servers["vault-ai"].(map[string]any)
	if vaultAI == nil {
		return "", nil // vault-ai may not be registered in this operator's settings
	}
	v, _ := vaultAI["contract_version"].(string)
	return v, nil
}

// TestContractVersionParity is the XREPO-01 Go-side parity gate (CLI-14).
// Reads the 3 surfaces, collects non-empty versions, asserts all collected
// versions are byte-identical strings. Catches consumer-side drift even
// when the bash walker is bypassed.
//
// Soft-skip semantics: in solo-repo CI environments where the vault-ai
// sibling is not checked out, this test t.Skip-s rather than fails. The
// XREPO-01 walker still enforces parity at three other invocation points
// (vault-ai pre-commit + vault-ai CI walker check-xrepo-contract.sh +
// workspace-cli pre-commit chained-shim) and this test re-asserts the
// invariant locally where ~/projects/vault-ai exists.
func TestContractVersionParity(t *testing.T) {
	root := workspaceRootForTest(t)

	vaultAIToolsPath := filepath.Join(root, "vault-ai", "_tooling", "mcp", "contract", "tools.json")
	if _, err := os.Stat(vaultAIToolsPath); errors.Is(err, fs.ErrNotExist) {
		t.Skipf("vault-ai sibling repo not present at %s — XREPO-01 parity enforced upstream (vault-ai CI walker + workspace-cli pre-commit). Set WORKSPACE_ROOT to a directory containing vault-ai/ to run this gate.", root)
	}

	vaultAI, err := readVaultAITools(root)
	if err != nil {
		t.Fatalf("vault-ai tools.json (mandatory surface): %v", err)
	}
	wsCLI, err := readWorkspaceCLIDoc(root)
	if err != nil {
		t.Fatalf("workspace-cli vault-commands.md (mandatory surface): %v", err)
	}
	wfKit, err := readWorkflowKitSettings(root)
	if err != nil {
		t.Fatalf("workflow-kit settings.local.json (optional surface): %v", err)
	}

	// Collect non-empty versions. Soft-skipped surfaces return "".
	type sample struct {
		name string
		got  string
	}
	collected := []sample{
		{"vault-ai/_tooling/mcp/contract/tools.json", vaultAI},
		{"workspace-cli/docs/vault-commands.md", wsCLI},
	}
	if wfKit != "" {
		collected = append(collected, sample{"workflow-kit/.claude/settings.local.json", wfKit})
	} else {
		t.Logf("workflow-kit settings.local.json absent or empty — soft-skipped per CONTEXT D-16")
	}

	if len(collected) < 2 {
		t.Fatalf("collected only %d versions; need at least 2 to assert parity", len(collected))
	}

	first := collected[0].got
	for _, s := range collected[1:] {
		if s.got != first {
			t.Errorf("contract_version drift detected: %s = %q vs %s = %q (run vault-ai/_tooling/lint/check-xrepo-contract.sh)",
				collected[0].name, first, s.name, s.got)
		}
	}
}

// TestContractVersionParityMissingSource asserts that a missing or malformed
// vault-ai tools.json surfaces as an explicit error rather than a silent skip
// — the source-of-truth surface MUST be present, otherwise parity has nothing
// to compare against.
func TestContractVersionParityMissingSource(t *testing.T) {
	// Point at a tempdir where neither vault-ai nor any other surface exists.
	tmp := t.TempDir()
	_, err := readVaultAITools(tmp)
	if err == nil {
		t.Fatalf("readVaultAITools on tempdir returned nil error; want explicit ENOENT-style error")
	}
	if !strings.Contains(err.Error(), "tools.json") {
		t.Errorf("error should name the missing file; got %v", err)
	}
}

// TestContractVersionParityMalformedJSON asserts the error surface for a
// tools.json that exists but is not valid JSON.
func TestContractVersionParityMalformedJSON(t *testing.T) {
	tmp := t.TempDir()
	contractDir := filepath.Join(tmp, "vault-ai", "_tooling", "mcp", "contract")
	if err := os.MkdirAll(contractDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(contractDir, "tools.json"), []byte("{not valid json"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, err := readVaultAITools(tmp)
	if err == nil {
		t.Fatalf("malformed JSON: readVaultAITools returned nil error")
	}
	if !strings.Contains(err.Error(), "parse") {
		t.Errorf("error should mention 'parse'; got %v", err)
	}
}

// TestContractVersionParitySoftSkipsWorkflowKit asserts the soft-skip
// behavior: if workflow-kit/.claude/settings.local.json is absent,
// readWorkflowKitSettings returns ("", nil) so TestContractVersionParity
// passes on the operator-environment subset that lacks workflow-kit. Tests
// that the file-absence path is handled without an error.
func TestContractVersionParitySoftSkipsWorkflowKit(t *testing.T) {
	tmp := t.TempDir()
	got, err := readWorkflowKitSettings(tmp)
	if err != nil {
		t.Fatalf("absent workflow-kit returned error; want soft-skip (empty + nil); got %v", err)
	}
	if got != "" {
		t.Errorf("absent workflow-kit returned %q; want empty string", got)
	}
}
