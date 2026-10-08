package cmd

// vault_doctor_predict.go — `ws vault predict-bulk-load` leaf (Phase 23 Plan 23-07).
//
// Read-only subcommand that queries the predict_bulk_load MCP tool and
// displays audit chain growth projections for a given batch size.
// Registered as a sibling of `ws vault doctor` under `ws vault`.
//
// READ-ONLY: no state mutations, no --fix or --kill flags.
// Per memory feedback_no_auto_state_mutation.
//
// Output modes:
//   - default (human) — two key/value blocks: the current rows per stream
//     and their total, then the projection
//   - --json — JSON object with prediction fields

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"time"

	"github.com/rtxnik/workspace-cli/internal/output"
	"github.com/spf13/cobra"
)

// predictResult is the structured response from the predict_bulk_load MCP tool.
type predictResult struct {
	CurrentRowsPerStream  map[string]int `json:"current_rows_per_stream"`
	ProjectedNewRows      int            `json:"projected_new_rows"`
	EstimatedDedupSeconds float64        `json:"estimated_dedup_seconds"`
	ProjectedSegmentCount int            `json:"projected_segment_count"`
}

// Package-level seam for tests. Production wires to predictMCPCallImpl;
// unit tests overwrite this to inject mocked MCP responses.
var predictMCPCallFn = predictMCPCallImpl

// predictCallToolFn is a package-level seam so unit tests can exercise the
// envelope-handling branch without spawning a live MCP subprocess.
var predictCallToolFn = callVaultTool

// predictMCPCallImpl is the production implementation that calls the
// predict_bulk_load MCP tool via the stdio transport.
func predictMCPCallImpl(count int) (*predictResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	env, err := predictCallToolFn(ctx, "ws-vault-predict-bulk-load", "predict_bulk_load", map[string]any{
		"count": count,
	})
	if err != nil {
		return nil, fmt.Errorf("MCP call predict_bulk_load: %w", err)
	}
	if env == nil {
		return nil, fmt.Errorf("MCP call predict_bulk_load: nil envelope")
	}
	if !env.OK {
		// Preserve the envelope's error code through the documented 0-7 exit
		// mapping instead of collapsing every failure to a bare exit 1.
		if env.Error != nil {
			return nil, vaultErrExit("predict-bulk-load", env.Error)
		}
		return nil, &cliErrorWithExit{
			code: env.ExitCode(),
			msg:  "predict-bulk-load: backend reported failure without error details",
		}
	}
	result := &predictResult{}
	if err := json.Unmarshal(env.Data, result); err != nil {
		return nil, fmt.Errorf("parse predict_bulk_load response: %w", err)
	}
	return result, nil
}

func newVaultDoctorPredictCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "predict-bulk-load",
		Short: "Project audit chain growth for bulk note creation (read-only)",
		Long: "Query the predict_bulk_load MCP tool to estimate audit chain growth, " +
			"dedup processing time, and Qdrant segment count for a given number of notes. " +
			"Read-only — no state mutations.",
		Annotations: vaultAnnotation,
		Args:        cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cmd.SilenceUsage = true

			count, err := strconv.Atoi(args[0])
			if err != nil || count <= 0 {
				return fmt.Errorf("predict-bulk-load: count must be a positive integer, got %q", args[0])
			}

			result, err := predictMCPCallFn(count)
			if err != nil {
				// An envelope error already carries a leaf-prefixed
				// *cliErrorWithExit with the mapped exit code; return it
				// unwrapped so Execute() routes the code and the leaf name is
				// not duplicated. Transport/parse errors are plain — prefix them.
				var coded *cliErrorWithExit
				if errors.As(err, &coded) {
					return err
				}
				return fmt.Errorf("predict-bulk-load: %w", err)
			}

			jsonFlag, _ := cmd.Flags().GetBool("json")
			if jsonFlag {
				return output.WriteJSON(cmd.OutOrStdout(), result)
			}

			current, projection := predictReport(count, result)
			return writeReport(cmd.OutOrStdout(), output.Out(), current, projection)
		},
	}
	return cmd
}

// predictReport is ws vault predict-bulk-load's report (phase-5 §3.9): the
// current rows, one pair per stream in name order and then their total, and
// the projection for count notes. The hierarchy is in the two blocks, not in
// a key's leading spaces, which a KV loses when it stacks a key above its
// value.
func predictReport(count int, r *predictResult) (current, projection output.KV) {
	streams := make([]string, 0, len(r.CurrentRowsPerStream))
	total := 0
	for name, n := range r.CurrentRowsPerStream {
		streams = append(streams, name)
		total += n
	}
	sort.Strings(streams) // Go randomises a map's iteration
	pairs := make([]output.Fact, 0, len(streams)+1)
	for _, name := range streams {
		pairs = append(pairs, output.Fact{K: name, V: strconv.Itoa(r.CurrentRowsPerStream[name])})
	}
	pairs = append(pairs, output.Fact{K: "total", V: strconv.Itoa(total)})
	current = output.KV{Title: "Current rows", Pairs: pairs}
	projection = output.KV{Title: "Projection for " + countOf(count, "note", "notes"), Pairs: []output.Fact{
		{K: "Projected New Rows", V: strconv.Itoa(r.ProjectedNewRows)},
		{K: "Estimated Dedup Time", V: fmt.Sprintf("%.2fs", r.EstimatedDedupSeconds)},
		{K: "Projected Segments", V: strconv.Itoa(r.ProjectedSegmentCount)},
	}}
	return current, projection
}
