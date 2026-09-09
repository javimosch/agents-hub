// Package update implements cli-update-spec v1.1: content-hash versioning, a
// verify-then-swap self-update, a throttled passive nudge, and self-install.
//
// It is adapted from bkn's internal/update, deliberately by copying rather
// than by sharing a library: the spec is the common thing, and two binaries
// that agree because they both implement the same twelve rules are easier to
// reason about than two binaries coupled through a dependency that has to
// stay compatible with both.
//
// The published artifact lives wherever AHUB_UPDATE_URL points -- by default
// a bkn file namespace, because bkn already serves files publicly and there
// is no reason for this tool to need infrastructure of its own.
package update

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

var (
	ErrNoArtifact = errors.New("no artifact is published for this tool")
	ErrVerify     = errors.New("the download does not match the advertised version")
	ErrSmokeTest  = errors.New("the downloaded binary does not run")
	ErrPermission = errors.New("cannot write to that directory")
)

// Tool is the installed binary's name.
const Tool = "ahub"

// DefaultServer is the bkn instance that publishes this tool.
const DefaultServer = "https://bkn.intrane.fr"

// Release is the version document.
type Release struct {
	OK       bool   `json:"ok"`
	Version  string `json:"version"`
	Download string `json:"download"`
	SHA256   string `json:"sha256,omitempty"`
	Error    string `json:"error,omitempty"`
}

// Server resolves the base URL updates are fetched from.
func Server() string {
	if s := os.Getenv("AHUB_SERVER"); s != "" {
		return strings.TrimSuffix(s, "/")
	}
	return DefaultServer
}

// VersionURL is the document describing the published artifact.
func VersionURL() string {
	if u := os.Getenv("AHUB_UPDATE_URL"); u != "" {
		return u
	}
	return Server() + "/v1/files/ahub/ahub.json"
}

// HashFile returns sha256[:12] and the full digest of a file.
func HashFile(path string) (short, full string, err error) {
	f, err := os.Open(path)
	if err != nil {
		return "", "", err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", "", err
	}
	full = hex.EncodeToString(h.Sum(nil))
	return full[:12], full, nil
}

// Self resolves the running binary, following symlinks so an installed copy
// reached through a link hashes the real file.
func Self() (string, error) {
	self, err := os.Executable()
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(self); err == nil {
		self = resolved
	}
	return self, nil
}

// LocalVersion is the content hash of the running binary.
func LocalVersion() (string, error) {
	self, err := Self()
	if err != nil {
		return "", err
	}
	short, _, err := HashFile(self)
	return short, err
}

// Fetch asks the server what it is publishing.
func Fetch(timeout time.Duration) (Release, error) {
	client := &http.Client{Timeout: timeout}
	resp, err := client.Get(VersionURL())
	if err != nil {
		return Release{}, err
	}
	defer resp.Body.Close()

	var rel Release
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&rel); err != nil {
		return Release{}, fmt.Errorf("%w: %s did not return a version document", ErrNoArtifact, VersionURL())
	}
	if resp.StatusCode == http.StatusNotFound || !rel.OK {
		if rel.Error != "" {
			return Release{}, fmt.Errorf("%w: %s", ErrNoArtifact, rel.Error)
		}
		return Release{}, ErrNoArtifact
	}
	return rel, nil
}

// Result reports what an update did.
type Result struct {
	Updated bool   `json:"updated"`
	From    string `json:"from"`
	To      string `json:"to"`
	Path    string `json:"path,omitempty"`
	Backup  string `json:"backup,omitempty"`
}

// Apply performs the full check → download → verify → smoke-test → swap.
func Apply(force bool, log func(string, ...any)) (Result, error) {
	local, err := LocalVersion()
	if err != nil {
		return Result{}, err
	}
	rel, err := Fetch(20 * time.Second)
	if err != nil {
		return Result{}, err
	}
	if rel.Version == local && !force {
		return Result{Updated: false, From: local, To: local}, nil
	}

	self, err := Self()
	if err != nil {
		return Result{}, err
	}

	// Staged beside the target, never in /tmp: a rename across filesystems
	// fails, and a copy would not be atomic. The swap is a rename for the
	// same reason it is in every other tool here -- you cannot truncate-write
	// the inode you are currently executing (ETXTBSY), but you can move the
	// directory entry out from under it.
	staged := self + ".new"
	log("[update] downloading %s", rel.Version)
	if err := download(rel, staged); err != nil {
		os.Remove(staged)
		return Result{}, err
	}
	defer os.Remove(staged)

	short, full, err := HashFile(staged)
	if err != nil {
		return Result{}, err
	}
	if short != rel.Version || (rel.SHA256 != "" && full != rel.SHA256) {
		return Result{}, fmt.Errorf("%w: got %s, expected %s", ErrVerify, short, rel.Version)
	}
	if err := os.Chmod(staged, 0o755); err != nil {
		return Result{}, err
	}

	// A truncated publish can still be self-consistent: the hash matches
	// because it was computed over the truncated bytes. Running it is the
	// only check that catches that.
	log("[update] verifying %s runs", rel.Version)
	if err := smokeTest(staged); err != nil {
		return Result{}, err
	}

	backup := self + ".bak"
	if err := os.Rename(self, backup); err != nil {
		return Result{}, err
	}
	if err := os.Rename(staged, self); err != nil {
		// Put the working binary back before reporting: a failed update must
		// never leave a machine without the tool.
		_ = os.Rename(backup, self)
		return Result{}, err
	}
	log("[update] %s -> %s (previous kept at %s)", local, rel.Version, backup)
	return Result{Updated: true, From: local, To: rel.Version, Path: self, Backup: backup}, nil
}

func download(rel Release, dest string) error {
	url := rel.Download
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		url = Server() + "/" + strings.TrimPrefix(url, "/")
	}
	client := &http.Client{Timeout: 5 * time.Minute}
	resp, err := client.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: HTTP %d", url, resp.StatusCode)
	}
	f, err := os.OpenFile(dest, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(f, resp.Body)
	return err
}

// smokeTest runs the staged binary's cheapest command.
func smokeTest(path string) error {
	cmd := exec.Command(path, "version")
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%w: %v", ErrSmokeTest, err)
	}
	return nil
}
