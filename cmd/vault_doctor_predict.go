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
//   - default (human) — table with Metric / Value columns
//   - --json — JSON object with prediction fields

import (
	"context"
	"encoding/json"
	"fmt"
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

// predictMCPCallImpl is the production implementation that calls the
// predict_bulk_load MCP tool via the stdio transport.
func predictMCPCallImpl(count int) (*predictResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	env, err := callVaultTool(ctx, "ws-vault-predict-bulk-load", "predict_bulk_load", map[string]any{
		"count": count,
	})
	if err != nil {
		return nil, fmt.Errorf("MCP call predict_bulk_load: %w", err)
	}
	if !env.OK {
		msg := "unknown error"
		if env.Error != nil {
			msg = fmt.Sprintf("%s: %s", env.Error.Code, env.Error.Message)
		}
		return nil, fmt.Errorf("MCP error: %s", msg)
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
				return fmt.Errorf("predict-bulk-load: %w", err)
			}

			jsonFlag, _ := cmd.Flags().GetBool("json")
			if jsonFlag {
				return output.WriteJSON(cmd.OutOrStdout(), result)
			}

			// Table output
			w := cmd.OutOrStdout()
			_, _ = fmt.Fprintf(w, "%-30s %s\n", "Metric", "Value")
			_, _ = fmt.Fprintf(w, "%-30s %s\n", "------", "-----")

			// Current rows per stream
			totalCurrent := 0
			for _, v := range result.CurrentRowsPerStream {
				totalCurrent += v
			}
			_, _ = fmt.Fprintf(w, "%-30s %d\n", "Current Total Rows", totalCurrent)

			// Per-stream breakdown
			for stream, count := range result.CurrentRowsPerStream {
				_, _ = fmt.Fprintf(w, "%-30s %d\n", fmt.Sprintf("  %s", stream), count)
			}

			_, _ = fmt.Fprintf(w, "%-30s %d\n", "Projected New Rows", result.ProjectedNewRows)
			_, _ = fmt.Fprintf(w, "%-30s %.2fs\n", "Estimated Dedup Time", result.EstimatedDedupSeconds)
			_, _ = fmt.Fprintf(w, "%-30s %d\n", "Projected Segments", result.ProjectedSegmentCount)

			return nil
		},
	}
	return cmd
}
