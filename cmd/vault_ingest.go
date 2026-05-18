package cmd

// vault_ingest.go — `ws vault ingest <file>` leaf (CLI-07).
//
// Wraps the MCP `create_note` tool with `check_dedup_before_create=true` by
// default per CONTEXT D-24. Parses the operator's markdown file's YAML
// frontmatter into the required {type, zone, frontmatter, body} contract
// fields.
//
// Dedup-override discipline (CONTEXT D-24 + Phase 22 D-09/D-10):
//   --dedup-force  -> args.ConfirmDedupOverride = true
//   --reason <txt> -> args.Reason; REQUIRED whenever --dedup-force set
//   --yes          -> skip the operator confirmation prompt
//                     (default behavior fires output.Confirm; declining
//                      Aborted with exit 1; create_note NOT called)
//
// NOTE on contract-field naming (Rule 1 deviation tracked in SUMMARY):
//   The plan draft <interfaces> proposed args.DedupForce + args.DedupOverrideReason,
//   but the live tools.json v1.3.0 (and the generated CreateNoteArgs struct in
//   internal/mcp/types.go) names them ConfirmDedupOverride + Reason. The
//   user-facing flag stays --dedup-force (Phase 17 / D-24 operator vocabulary)
//   but the contract field set is ConfirmDedupOverride+Reason at call time.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/rtxnik/workspace-cli/internal/mcp"
	"github.com/rtxnik/workspace-cli/internal/output"
	"github.com/spf13/cobra"
)

// Test seams: production wires to runVaultIngest + output.Confirm; unit tests
// override with closures that emulate MCP responses + operator prompt decisions.
var (
	vaultIngestCallFn    = runVaultIngest
	vaultIngestConfirmFn = output.Confirm
)

func runVaultIngest(ctx context.Context, root *cobra.Command, cargs mcp.CreateNoteArgs) (*mcp.Envelope, error) {
	cl, err := mcp.NewClient(ctx, mcp.Options{
		VaultAIRepoRoot: os.Getenv("VAULT_AI_REPO_ROOT"),
		Version:         root.Version,
	})
	if err != nil {
		return nil, fmt.Errorf("spawn MCP client: %w", err)
	}
	stop := mcp.InstallSignalForward(cl)
	defer stop()
	defer cl.Close(ctx)

	env, err := cl.Call(ctx, "create_note", &cargs)
	if err != nil {
		return nil, fmt.Errorf("MCP roundtrip: %w", err)
	}
	return env, nil
}

// splitFrontmatter parses a markdown file with YAML frontmatter delimited by
// `---` lines and returns (frontmatter map, body string, error). Body excludes
// the trailing `---` delimiter. Errors when the file is missing the opening
// fence or the closing fence.
//
// Minimal hand-rolled parser to avoid adding a direct YAML dep for what is
// otherwise the smallest possible operational surface. Only key:value scalar
// pairs are supported; nested mappings or lists land in the map as raw
// strings (which the MCP server's per-type schema validator will re-parse).
// That trade-off is documented because operator-authored ingest files are
// expected to use the canonical zettel/source/etc. shape with flat
// frontmatter; complex frontmatter should go through the agent surface.
func splitFrontmatter(raw []byte) (map[string]any, string, error) {
	const fence = "---"
	if !bytes.HasPrefix(raw, []byte(fence+"\n")) && !bytes.HasPrefix(raw, []byte(fence+"\r\n")) {
		return nil, "", fmt.Errorf("ingest: missing opening `---` frontmatter fence")
	}
	// Skip the opening fence line.
	rest := raw[len(fence):]
	if rest[0] == '\r' {
		rest = rest[1:]
	}
	if rest[0] == '\n' {
		rest = rest[1:]
	}
	// Find the closing `---` line.
	closeIdx := bytes.Index(rest, []byte("\n---"))
	if closeIdx < 0 {
		return nil, "", fmt.Errorf("ingest: missing closing `---` frontmatter fence")
	}
	fmRaw := rest[:closeIdx]
	body := rest[closeIdx+len("\n---"):]
	if len(body) > 0 && body[0] == '\r' {
		body = body[1:]
	}
	if len(body) > 0 && body[0] == '\n' {
		body = body[1:]
	}

	fmMap := make(map[string]any)
	for _, line := range strings.Split(string(fmRaw), "\n") {
		line = strings.TrimRight(line, "\r")
		// Skip blanks + YAML comments.
		t := strings.TrimSpace(line)
		if t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		// Only flat scalar key:value pairs at column 0 supported.
		colon := strings.Index(line, ":")
		if colon < 0 {
			continue
		}
		key := strings.TrimSpace(line[:colon])
		val := strings.TrimSpace(line[colon+1:])
		// Strip surrounding quotes if present.
		val = strings.Trim(val, `"'`)
		if key == "" {
			continue
		}
		fmMap[key] = val
	}
	return fmMap, string(body), nil
}

func newVaultIngestCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:         "ingest <file>",
		Short:       "Ingest a markdown file via MCP create_note (dedup-gated)",
		Long:        "Reads a markdown file (YAML frontmatter + body), creates a note via MCP create_note with the dedup gate enabled per CONTEXT D-24. Override via --dedup-force --reason \"<text>\" (operator confirmation prompt fires unless --yes is set). On DEDUP_BLOCKED returns exit 6 with envelope.error.details on stderr.",
		Annotations: vaultAnnotation,
		Args:        cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cmd.SilenceUsage = true
			ctx := cmd.Context()
			if ctx == nil {
				ctx = context.Background()
			}

			path := args[0]
			raw, err := os.ReadFile(path)
			if err != nil {
				return &cliErrorWithExit{code: 1, msg: fmt.Sprintf("ingest: read %s: %v", path, err)}
			}
			fmMap, body, err := splitFrontmatter(raw)
			if err != nil {
				return &cliErrorWithExit{code: 1, msg: err.Error()}
			}
			noteType, _ := fmMap["type"].(string)
			zone, _ := fmMap["zone"].(string)
			if noteType == "" {
				return &cliErrorWithExit{code: 1, msg: "ingest: frontmatter missing required `type` field"}
			}
			if zone == "" {
				return &cliErrorWithExit{code: 1, msg: "ingest: frontmatter missing required `zone` field"}
			}

			dedupForce, _ := cmd.Flags().GetBool("dedup-force")
			yes, _ := cmd.Flags().GetBool("yes")
			reason, _ := cmd.Flags().GetString("reason")
			reason = strings.TrimSpace(reason)

			if dedupForce {
				if reason == "" {
					return &cliErrorWithExit{
						code: 1,
						msg:  "ingest: --dedup-force requires --reason \"<operator override reason>\" (audited via Phase 17 D-12 dedup_override stream)",
					}
				}
				if !yes {
					title := fmt.Sprintf("Override dedup gate for %q?", path)
					desc := "This will write the note bypassing the similarity-based DEDUP_BLOCKED. The reason is audited verbatim per Phase 17 D-12 dedup_override stream."
					if !vaultIngestConfirmFn(title, desc) {
						output.Info("Aborted")
						return &cliErrorWithExit{code: 1, msg: "ingest: aborted by operator at dedup-force confirmation prompt"}
					}
				}
			}

			cargs := mcp.CreateNoteArgs{
				Type:                   noteType,
				Zone:                   zone,
				Frontmatter:            fmMap,
				Body:                   body,
				CheckDedupBeforeCreate: true,
				ConfirmDedupOverride:   dedupForce,
				Reason:                 reason,
			}

			env, err := vaultIngestCallFn(ctx, cmd.Root(), cargs)
			if err != nil {
				return fmt.Errorf("ingest: %w", err)
			}
			if env == nil {
				return fmt.Errorf("ingest: nil envelope")
			}
			if env.Error != nil {
				// Surface envelope.error.details on stderr so operator sees
				// the existing-id + similarity + threshold per CONTEXT D-24.
				if len(env.Error.Details) > 0 {
					var pretty bytes.Buffer
					if err := json.Indent(&pretty, env.Error.Details, "  ", "  "); err == nil {
						fmt.Fprintf(cmd.ErrOrStderr(), "%s details:\n  %s\n", env.Error.Code, pretty.String())
					} else {
						fmt.Fprintf(cmd.ErrOrStderr(), "%s details: %s\n", env.Error.Code, string(env.Error.Details))
					}
				}
				return &cliErrorWithExit{
					code: mcp.MapErrorCodeToExitCode(env.Error.Code),
					msg:  fmt.Sprintf("ingest: %s: %s", env.Error.Code, env.Error.Message),
				}
			}

			jsonFlag, _ := cmd.Flags().GetBool("json")
			return renderCoverageReport(cmd.OutOrStdout(), env.Data, jsonFlag)
		},
	}
	cmd.Flags().Bool("dedup-force", false, "Override DEDUP_BLOCKED via confirm_dedup_override=true (requires --reason; CONTEXT D-24)")
	cmd.Flags().Bool("yes", false, "Skip operator confirmation prompt for --dedup-force (Phase 22 D-09/D-10 manual-recovery discipline)")
	cmd.Flags().String("reason", "", "Operator override reason (REQUIRED with --dedup-force; audited verbatim per Phase 17 D-12)")
	return cmd
}
