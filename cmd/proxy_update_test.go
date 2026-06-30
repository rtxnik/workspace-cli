package cmd

import (
	"errors"
	"strings"
	"testing"
)

// A failed transactional recreate has rolled back to the previous proxy, so the
// update outcome must be a WARNING that says the previous version is still
// serving -- not a success.
func TestRecreateUpdateOutcome_RolledBack(t *testing.T) {
	msg, warn := recreateUpdateOutcome(errors.New("new proxy failed: boom -- rolled back"))
	if !warn {
		t.Fatal("a failed recreate must be surfaced as a warning, not success")
	}
	if !strings.Contains(msg, "rolled back") || !strings.Contains(msg, "previous version") {
		t.Fatalf("rolled-back message must say the previous version is still serving; got: %q", msg)
	}
}

func TestRecreateUpdateOutcome_Success(t *testing.T) {
	msg, warn := recreateUpdateOutcome(nil)
	if warn {
		t.Fatal("a successful recreate must not warn")
	}
	if !strings.Contains(msg, "new version") {
		t.Fatalf("success message should mention the new version; got: %q", msg)
	}
}
