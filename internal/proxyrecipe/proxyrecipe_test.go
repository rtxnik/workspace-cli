package proxyrecipe

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

func sha256hex(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// writeRecipe creates <dir>/proxy/{Dockerfile,entrypoint.sh} and returns their hashes.
func writeRecipe(t *testing.T, dir, dockerfile, entrypoint string) (string, string) {
	t.Helper()
	proxy := filepath.Join(dir, "proxy")
	if err := os.MkdirAll(proxy, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(proxy, "Dockerfile"), []byte(dockerfile), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(proxy, "entrypoint.sh"), []byte(entrypoint), 0o644); err != nil {
		t.Fatal(err)
	}
	return sha256hex([]byte(dockerfile)), sha256hex([]byte(entrypoint))
}

func TestVerify_MatchingRecipeIsOK(t *testing.T) {
	dir := t.TempDir()
	dh, eh := writeRecipe(t, dir, "FROM ubuntu\n", "#!/bin/sh\n")
	p := Pinned{DatapathMode: "tproxy", Files: map[string]string{"Dockerfile": dh, "entrypoint.sh": eh}}

	res, err := verify(p, filepath.Join(dir, "proxy"))
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if !res.OK {
		t.Fatalf("expected OK, got mismatches=%v missing=%v", res.Mismatches, res.Missing)
	}
	if res.Mode != "tproxy" {
		t.Errorf("Mode = %q, want tproxy", res.Mode)
	}
	if res.CombinedDigest == "" {
		t.Error("CombinedDigest must be set on a matching recipe")
	}
}

func TestVerify_DriftedFileIsReported(t *testing.T) {
	dir := t.TempDir()
	dh, eh := writeRecipe(t, dir, "FROM ubuntu\n", "#!/bin/sh\n")
	// Pin the Dockerfile to a different hash to simulate drift.
	p := Pinned{DatapathMode: "tproxy", Files: map[string]string{"Dockerfile": "deadbeef", "entrypoint.sh": eh}}
	_ = dh

	res, err := verify(p, filepath.Join(dir, "proxy"))
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if res.OK {
		t.Fatal("expected drift (OK=false)")
	}
	if len(res.Mismatches) != 1 || res.Mismatches[0].File != "Dockerfile" {
		t.Errorf("expected one Dockerfile mismatch, got %v", res.Mismatches)
	}
}

func TestVerify_MissingFileFailsClosed(t *testing.T) {
	dir := t.TempDir()
	// Only write Dockerfile; entrypoint.sh is absent.
	proxy := filepath.Join(dir, "proxy")
	if err := os.MkdirAll(proxy, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(proxy, "Dockerfile"), []byte("FROM ubuntu\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := Pinned{DatapathMode: "tproxy", Files: map[string]string{
		"Dockerfile":    sha256hex([]byte("FROM ubuntu\n")),
		"entrypoint.sh": "whatever",
	}}

	res, err := verify(p, proxy)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if res.OK {
		t.Fatal("expected fail-closed on a missing file")
	}
	if len(res.Missing) != 1 || res.Missing[0] != "entrypoint.sh" {
		t.Errorf("expected entrypoint.sh missing, got %v", res.Missing)
	}
}

func TestLoad_EmbeddedPinParses(t *testing.T) {
	p, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if p.DatapathMode != "tproxy" {
		t.Errorf("DatapathMode = %q, want tproxy", p.DatapathMode)
	}
	if _, ok := p.Files["Dockerfile"]; !ok {
		t.Error("pin must list Dockerfile")
	}
	if _, ok := p.Files["entrypoint.sh"]; !ok {
		t.Error("pin must list entrypoint.sh")
	}
}
