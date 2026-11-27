package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The first published build was stale and carried the builder's home paths.
func TestPublishScriptBuildsACleanArtifactAndItsVersionDocument(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a release binary")
	}
	dir := t.TempDir()
	cmd := exec.Command("/bin/sh", "scripts/publish.sh", dir)
	cmd.Env = append(os.Environ(), "AHUB_PUBLISH_ALLOW_DIRTY=1")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("publish.sh: %v: %s", err, out)
	}
	artifact, err := os.ReadFile(filepath.Join(dir, "ahub"))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		OK                        bool
		Version, Download, SHA256 string
	}
	raw, err := os.ReadFile(filepath.Join(dir, "ahub.json"))
	if err == nil {
		err = json.Unmarshal(raw, &doc)
	}
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(artifact)
	full := hex.EncodeToString(sum[:])
	if !doc.OK || doc.SHA256 != full || doc.Version != full[:12] || doc.Download == "" {
		t.Errorf("version document %+v does not describe the artifact %s", doc, full)
	}
	here, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(artifact), here) {
		t.Error("the artifact contains the builder's source path")
	}
	version, err := exec.Command(filepath.Join(dir, "ahub"), "version").Output()
	if err != nil || strings.Contains(string(version), `"tool_version":"dev"`) {
		t.Errorf("the artifact is not stamped with a version: %s (%v)", version, err)
	}
}
