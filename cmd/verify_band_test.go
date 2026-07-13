package cmd

import (
	"testing"

	"github.com/rtxnik/workspace-cli/internal/mcp"
)

// TestVerify_C_BandMappersDisagreeOnUnknown pins the invariant that every
// band-to-exit-code decision now flows through one fail-closed mapper:
// known bands map green->0, yellow->1, red->2, and any unrecognized or
// empty band yields exit 2 (fail-closed), never 0 (spurious healthy).
func TestVerify_C_BandMappersDisagreeOnUnknown(t *testing.T) {
	cases := []struct {
		band string
		want int
	}{
		{"green", 0},
		{"yellow", 1},
		{"red", 2},
		{"unrecognized-band", 2},
		{"", 2},
	}
	for _, c := range cases {
		if got := mcp.HealthBandExitCode(c.band); got != c.want {
			t.Errorf("HealthBandExitCode(%q) = %d; want %d", c.band, got, c.want)
		}
	}
}
