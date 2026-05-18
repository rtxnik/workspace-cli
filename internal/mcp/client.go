package mcp

// client.go implements the single subprocess-lifecycle chokepoint that all 10
// `ws vault` leaf commands route through per CONTEXT D-05 + RESEARCH §Pattern 1.
//
// Architectural invariants (all verified by tests in this package):
//
//  1. ONE place calls exec.CommandContext — leaves call mcp.NewClient.
//  2. The mark3labs/mcp-go library's Close() signals the LEADER process only
//     (verified at v0.52.0 against the library's stdio.go source per
//     RESEARCH §Pitfall 4). Because `uv run python ...` spawns multiple
//     descendants, our wrapper MUST signal the process GROUP itself via
//     syscall.Kill(-pgid, SIGTERM/SIGKILL).
//  3. Setpgid is configured on the subprocess via the library's
//     WithCommandFunc hook — the library does NOT expose SysProcAttr directly.
//     This is the architecturally-required injection point per RESEARCH
//     §Pattern 1; the Plan 18-01 verify gate greps for "WithCommandFunc".
//  4. Token transport: VAULT_AI_TOKEN read from operator env, scrubbed from
//     cmd.Env, then written into the child via fd 3 (cmd.ExtraFiles[0]).
//     Path-A per CONTEXT D-09a (operator decision, 2026-05-18).
//  5. signal.Notify(os.Interrupt) is forwarded to the process group so Ctrl-C
//     terminates the entire descendant tree, not just the uv leader.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"sync"
	"syscall"
	"time"

	mcpclient "github.com/mark3labs/mcp-go/client"
	mcptransport "github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
)

// lookPathFn is a package-level seam for tests to override exec.LookPath when
// asserting Pitfall 5 / fd-3-collision behavior. Production code uses
// exec.LookPath directly via this var.
var lookPathFn = exec.LookPath

// initializeTimeout caps how long we wait for the subprocess's initial MCP
// handshake (tools/list capability advertisement) before bailing. Cold start
// of `uv run python -m vault_ai.adapter_stdio.server --stdio` is typically
// 2-3s on a warm operator workstation; 15s leaves ample margin for cold venv
// resolution without hanging the cobra leaf indefinitely.
const initializeTimeout = 15 * time.Second

// closeGracePeriod is how long we let the library's c.Close() drain stdio +
// reap the leader before we escalate to SIGKILL on the process group. 5s
// matches RESEARCH §Pattern 1 L223-241 and the Pitfall 7 §How-to-avoid spec.
const closeGracePeriod = 5 * time.Second

// Options drives NewClient. Fields are documented inline so the cobra leaves'
// call sites stay self-explanatory.
type Options struct {
	// VaultAIRepoRoot is the absolute filesystem path to the vault-ai
	// checkout (sets cmd.Dir for the subprocess + the uv --project flag).
	// If empty, NewClient falls back to $VAULT_AI_REPO_ROOT and finally
	// ~/projects/vault-ai per CONTEXT D-08.
	VaultAIRepoRoot string

	// Version is the workspace-cli build version stamped into the MCP
	// Initialize handshake's ClientInfo.Version (so the server can log
	// "ws-vault/v1.2.3 connected" for forensic traceability).
	Version string
}

// Client wraps the mark3labs/mcp-go client and holds a reference to the
// underlying *exec.Cmd so Close can signal the process group via
// syscall.Kill(-pgid, ...). The library does not expose this surface.
type Client struct {
	c         *mcpclient.Client
	cmd       *exec.Cmd
	tokenPipe *os.File // path-A: closed on Close; never nil after successful NewClient
}

// NewClient spawns the vault-ai stdio MCP subprocess and performs the MCP
// Initialize handshake. On success, the returned *Client owns the subprocess
// and the caller MUST call Close to release it.
//
// Error paths:
//   - VAULT_AI_TOKEN unset → wrapped ErrMissingDependency (exit 4)
//   - uv binary at a non-regular path → "fd-3 collision risk" error
//   - os.Pipe() failure → wrapped error
//   - transport Start / Initialize failure → tokenPipe + library Close cleaned
//     up before return so we never leak fds or zombies
func NewClient(ctx context.Context, opts Options) (*Client, error) {
	token := os.Getenv("VAULT_AI_TOKEN")
	if token == "" {
		return nil, errMissingDepWrap("VAULT_AI_TOKEN unset; provision via chezmoi+age per ADR-ai-06")
	}

	// Resolve the vault-ai repo root with the documented fallback chain.
	repoRoot := opts.VaultAIRepoRoot
	if repoRoot == "" {
		repoRoot = os.Getenv("VAULT_AI_REPO_ROOT")
	}
	if repoRoot == "" {
		if home, err := os.UserHomeDir(); err == nil {
			repoRoot = home + "/projects/vault-ai"
		}
	}

	// Resolve uv via the test-overridable seam.
	uvPath, err := lookPathFn("uv")
	if err != nil {
		return nil, errMissingDepWrap(fmt.Sprintf("uv not on PATH: %v", err))
	}
	// Pitfall 5 / golang/go #66654 guard.
	if err := CheckUVPath(uvPath); err != nil {
		return nil, err
	}

	// fd-3 token pipe (path-A).
	tokenR, err := NewTokenPipe(token)
	if err != nil {
		return nil, err
	}

	// Capture the *exec.Cmd via the library's documented WithCommandFunc hook
	// — this is the only seam for Setpgid + ExtraFiles + cmd.Env + cmd.Dir
	// per RESEARCH §Pattern 1 L194-203.
	var capturedCmd *exec.Cmd
	cmdFunc := func(cmdCtx context.Context, command string, env []string, args []string) (*exec.Cmd, error) {
		c := exec.CommandContext(cmdCtx, command, args...)
		// Defense-in-depth: strip VAULT_AI_TOKEN out of the child env even
		// though the library passed our os.Environ() in — guards against
		// the operator (or a wrapper script) leaking the var via another
		// env-export path. Verified by TestTokenAbsentFromEnviron.
		c.Env = stripToken(env, "VAULT_AI_TOKEN")
		c.ExtraFiles = []*os.File{tokenR}
		c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		c.Dir = repoRoot
		capturedCmd = c
		return c, nil
	}

	// Note: the adapter_stdio CLI is stdio-by-default and does NOT accept a
	// --stdio flag (verified at vault-ai HEAD 2026-05-18 via
	// `python -m vault_ai.adapter_stdio.server --help` — only -h and --config
	// are recognized). CONTEXT D-08 + RESEARCH §Pattern 1 cite a --stdio
	// argument that the live source rejects; we omit it per `feedback_verify_
	// before_claim`. Auto-fix Rule 1: discovered during integration smoke
	// when the handshake closed transport because argparse exited non-zero.
	transport := mcptransport.NewStdioWithOptions(
		uvPath,
		os.Environ(),
		[]string{
			"run",
			"--project", repoRoot + "/_tooling/mcp",
			"--",
			"python", "-m", "vault_ai.adapter_stdio.server",
		},
		mcptransport.WithCommandFunc(cmdFunc),
	)

	c := mcpclient.NewClient(transport)
	if err := c.Start(ctx); err != nil {
		_ = tokenR.Close()
		return nil, fmt.Errorf("MCP transport start: %w", err)
	}

	// Bounded Initialize handshake — separate ctx so a slow Initialize does
	// not eat into the leaf command's own ctx deadline.
	initCtx, cancel := context.WithTimeout(ctx, initializeTimeout)
	defer cancel()
	if _, err := c.Initialize(initCtx, mcp.InitializeRequest{
		Params: mcp.InitializeParams{
			ProtocolVersion: "2025-06-18",
			ClientInfo: mcp.Implementation{
				Name:    "ws-vault",
				Version: opts.Version,
			},
		},
	}); err != nil {
		// Best-effort tear down on Initialize failure — never leak the
		// subprocess or the token pipe.
		_ = c.Close()
		if capturedCmd != nil && capturedCmd.Process != nil {
			if pgid, pgerr := syscall.Getpgid(capturedCmd.Process.Pid); pgerr == nil {
				_ = syscall.Kill(-pgid, syscall.SIGKILL)
			}
		}
		_ = tokenR.Close()
		return nil, fmt.Errorf("MCP Initialize handshake: %w", err)
	}

	return &Client{c: c, cmd: capturedCmd, tokenPipe: tokenR}, nil
}

// Close tears down the subprocess + token pipe per RESEARCH §Pattern 1
// L223-241. The library's c.Close() only signals the leader; we MUST signal
// the process group ourselves so descendant Python processes (FastMCP tool
// handlers spawned by the adapter) terminate cleanly.
//
// Sequence:
//  1. defer tokenPipe.Close() — never leak fds
//  2. SIGTERM the process group (-pgid)
//  3. Race library Close (which drains stdio + reaps the leader) against a
//     5s grace timer
//  4. On timeout: escalate to SIGKILL on the process group, then wait for
//     library Close to return so we do not leak a goroutine
func (cl *Client) Close(ctx context.Context) error {
	if cl == nil {
		return nil
	}
	if cl.tokenPipe != nil {
		defer cl.tokenPipe.Close()
	}

	// Signal the process group first so descendants get the term before the
	// leader's stdio drain completes.
	if cl.cmd != nil && cl.cmd.Process != nil {
		if pgid, err := syscall.Getpgid(cl.cmd.Process.Pid); err == nil {
			_ = syscall.Kill(-pgid, syscall.SIGTERM)
		}
	}

	done := make(chan error, 1)
	go func() {
		done <- cl.c.Close()
	}()

	select {
	case err := <-done:
		return err
	case <-time.After(closeGracePeriod):
		// Escalate. Library may still be blocked on stdio drain; SIGKILL the
		// group, then wait for the library's Close to return so we don't
		// leak the goroutine.
		if cl.cmd != nil && cl.cmd.Process != nil {
			if pgid, err := syscall.Getpgid(cl.cmd.Process.Pid); err == nil {
				_ = syscall.Kill(-pgid, syscall.SIGKILL)
			}
		}
		<-done
		return fmt.Errorf("subprocess teardown timed out after %s; sent SIGKILL to process group", closeGracePeriod)
	}
}

// ListedTool is the workspace-cli-side projection of mcp.Tool from the
// mark3labs/mcp-go library. We expose only the fields the `ws vault` leaves
// actually consume (Name + Properties of the input schema) so consumer code
// does not depend on the upstream library's struct layout.
type ListedTool struct {
	// Name is the tool name (e.g. "create_note").
	Name string
	// InputProperties is the JSON Schema "properties" map of the tool's
	// input schema. Values are arbitrary because JSON Schema is heterogeneous;
	// callers typically only check key presence (e.g. ws vault status uses
	// this to probe whether create_note advertises check_dedup_before_create).
	InputProperties map[string]any
}

// ListTools issues the MCP `tools/list` JSON-RPC method and returns the
// advertised tool catalogue. This is the cheapest MCP roundtrip — no
// embedder/qdrant load — and is the canonical liveness signal for
// `ws vault status` (signal 1) and the dedup-gate readiness probe (signal 5)
// per CONTEXT D-21.
//
// Returns nil + error on transport failure or when the client is closed.
func (cl *Client) ListTools(ctx context.Context) ([]ListedTool, error) {
	if cl == nil || cl.c == nil {
		return nil, fmt.Errorf("Client.ListTools on nil/closed client")
	}
	res, err := cl.c.ListTools(ctx, mcp.ListToolsRequest{})
	if err != nil {
		return nil, fmt.Errorf("MCP ListTools: %w", err)
	}
	out := make([]ListedTool, 0, len(res.Tools))
	for _, t := range res.Tools {
		out = append(out, ListedTool{
			Name:            t.Name,
			InputProperties: t.InputSchema.Properties,
		})
	}
	return out, nil
}

// Call invokes an MCP tool by name with the given arguments and decodes the
// response into a workspace-cli Envelope (the wire shape from
// workspace-cli/docs/vault-commands.md v1.3.0).
//
// MCP tool responses come back as a CallToolResult with a Content slice;
// vault-ai's FastMCP handlers emit a single TextContent block whose Text is
// the JSON envelope. We marshal/unmarshal that into our Envelope type so
// cobra leaves get a stable consumer surface that does not depend on the
// library's CallToolResult shape.
func (cl *Client) Call(ctx context.Context, tool string, args any) (*Envelope, error) {
	if cl == nil || cl.c == nil {
		return nil, fmt.Errorf("Client.Call on nil/closed client")
	}
	res, err := cl.c.CallTool(ctx, mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name:      tool,
			Arguments: args,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("MCP CallTool(%s): %w", tool, err)
	}
	if res == nil || len(res.Content) == 0 {
		return nil, fmt.Errorf("MCP CallTool(%s): empty response content", tool)
	}

	// Extract the JSON text payload from the first TextContent block.
	// vault-ai's FastMCP handlers always emit exactly one TextContent block
	// per envelope contract.
	textPayload, err := extractTextPayload(res.Content[0])
	if err != nil {
		return nil, fmt.Errorf("MCP CallTool(%s) content decode: %w", tool, err)
	}

	var env Envelope
	if err := json.Unmarshal([]byte(textPayload), &env); err != nil {
		return nil, fmt.Errorf("MCP CallTool(%s) envelope JSON unmarshal: %w", tool, err)
	}
	return &env, nil
}

// extractTextPayload pulls the .text field out of a TextContent block. We
// handle both the typed mcp.TextContent shape and the more general
// map[string]any shape (mark3labs sometimes returns the latter when content
// type negotiation falls through).
func extractTextPayload(c mcp.Content) (string, error) {
	if tc, ok := c.(mcp.TextContent); ok {
		return tc.Text, nil
	}
	if tc, ok := c.(*mcp.TextContent); ok {
		return tc.Text, nil
	}
	// Fallback: marshal then unmarshal to extract .text generically.
	raw, err := json.Marshal(c)
	if err != nil {
		return "", fmt.Errorf("marshal content for text extraction: %w", err)
	}
	var generic struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &generic); err != nil {
		return "", fmt.Errorf("unmarshal generic content: %w", err)
	}
	if generic.Text == "" {
		return "", fmt.Errorf("content block has no text payload (type=%q)", generic.Type)
	}
	return generic.Text, nil
}

// InstallSignalForward installs an os.Interrupt + SIGTERM handler that
// forwards the signal to the subprocess process group via
// syscall.Kill(-pgid, SIGTERM). The returned stop func() detaches the handler
// — cobra leaves should defer stop() so the handler is removed on RunE return.
//
// The stop func is idempotent: calling it twice (or after the signal already
// fired) is a no-op rather than a panic, since defer-on-error paths may end
// up double-calling it.
func InstallSignalForward(cl *Client) (stop func()) {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)

	go func() {
		s, ok := <-sigCh
		if !ok {
			return
		}
		_ = s // surface to debugger if attached; the act of forwarding is what matters
		if cl == nil || cl.cmd == nil || cl.cmd.Process == nil {
			return
		}
		if pgid, err := syscall.Getpgid(cl.cmd.Process.Pid); err == nil {
			_ = syscall.Kill(-pgid, syscall.SIGTERM)
		}
	}()

	var once sync.Once
	return func() {
		once.Do(func() {
			signal.Stop(sigCh)
			close(sigCh)
		})
	}
}

// stripToken returns a copy of env with any entry of the form key+"="...
// removed. Pure function for testability; used by NewClient to scrub
// VAULT_AI_TOKEN out of cmd.Env before the subprocess starts (CONTEXT D-09
// defense-in-depth — verified by TestTokenAbsentFromEnviron).
func stripToken(env []string, key string) []string {
	if len(env) == 0 {
		return env
	}
	prefix := key + "="
	out := make([]string, 0, len(env))
	for _, e := range env {
		if len(e) >= len(prefix) && e[:len(prefix)] == prefix {
			continue
		}
		out = append(out, e)
	}
	return out
}
