package docker

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rtxnik/workspace-cli/internal/proxyrecipe"
)

func hasLabel(args []string, kv string) bool {
	for i, a := range args {
		if a == "--label" && i+1 < len(args) && args[i+1] == kv {
			return true
		}
	}
	return false
}

func TestBuildProxyArgs_OKStampsTruthfulLabels(t *testing.T) {
	cfg := testCfg()
	res := proxyrecipe.Result{OK: true, Mode: "tproxy", CombinedDigest: "deadbeef"}

	args, err := buildProxyArgs(cfg, "", res, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !hasLabel(args, LabelDatapath+"=tproxy") {
		t.Errorf("missing %s=tproxy in %v", LabelDatapath, args)
	}
	if !hasLabel(args, LabelRecipe+"=deadbeef") {
		t.Errorf("missing %s=deadbeef in %v", LabelRecipe, args)
	}
	if args[len(args)-1] != filepath.Join(cfg.ProfilesDir, "proxy") {
		t.Errorf("build context must be the last arg, got %v", args)
	}
}

func TestBuildProxyArgs_DriftWithoutAllowIsError(t *testing.T) {
	res := proxyrecipe.Result{OK: false, Mode: "tproxy", CombinedDigest: "x",
		Mismatches: []proxyrecipe.FileMismatch{{File: "entrypoint.sh"}}}

	_, err := buildProxyArgs(testCfg(), "", res, false)
	if err == nil {
		t.Fatal("expected drift to be a hard error without --allow-drift")
	}
	if !strings.Contains(err.Error(), "allow-drift") {
		t.Errorf("error must mention the --allow-drift escape hatch, got %v", err)
	}
}

func TestBuildProxyArgs_DriftWithAllowStampsUnverified(t *testing.T) {
	res := proxyrecipe.Result{OK: false, Mode: "tproxy", CombinedDigest: "x",
		Mismatches: []proxyrecipe.FileMismatch{{File: "entrypoint.sh"}}}

	args, err := buildProxyArgs(testCfg(), "", res, true)
	if err != nil {
		t.Fatalf("--allow-drift must not error: %v", err)
	}
	if !hasLabel(args, LabelDatapath+"=unverified") {
		t.Errorf("drifted build must stamp %s=unverified, got %v", LabelDatapath, args)
	}
}

func TestBuildProxyArgs_VersionAddsBuildArg(t *testing.T) {
	res := proxyrecipe.Result{OK: true, Mode: "tproxy", CombinedDigest: "x"}
	args, err := buildProxyArgs(testCfg(), "v9.9.9", res, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "--build-arg XRAY_VERSION=v9.9.9") {
		t.Errorf("expected XRAY_VERSION build-arg, got %v", args)
	}
}

// BuildProxyImage returns the drift error before shelling out to docker, so a
// drifted recipe is observable without a docker daemon.
func TestBuildProxyImage_DriftBlocksBeforeDocker(t *testing.T) {
	dir := t.TempDir()
	proxy := filepath.Join(dir, "proxy")
	if err := os.MkdirAll(proxy, 0o755); err != nil {
		t.Fatal(err)
	}
	// Files present but their content will not match the embedded pin.
	if err := os.WriteFile(filepath.Join(proxy, "Dockerfile"), []byte("drifted\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(proxy, "entrypoint.sh"), []byte("drifted\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := testCfg()
	cfg.ProfilesDir = dir

	err := BuildProxyImage(cfg, "", false)
	if err == nil {
		t.Fatal("expected drift error")
	}
	if !strings.Contains(err.Error(), "drift") {
		t.Errorf("expected a drift error, got %v", err)
	}
}
