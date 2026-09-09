package update

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// DefaultPrefix is where a binary installs itself: no sudo, and already on
// $PATH for most setups.
func DefaultPrefix() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "/usr/local/bin"
	}
	return filepath.Join(home, ".local", "bin")
}

// Install copies the running binary to prefix/ahub.
//
// A copy, not a move or a symlink: the original may be a build artifact or a
// file in Downloads that the caller deletes straight afterwards, and the
// installed copy has to survive that.
func Install(prefix string) (string, error) {
	if prefix == "" {
		prefix = DefaultPrefix()
	}
	self, err := Self()
	if err != nil {
		return "", err
	}
	dest := filepath.Join(prefix, Tool)
	if dest == self {
		// Installing over itself is a no-op rather than an error: an agent
		// re-running a setup step must never see a false failure.
		return dest, nil
	}
	if err := os.MkdirAll(prefix, 0o755); err != nil {
		return "", fmt.Errorf("%w: %s", ErrPermission, prefix)
	}
	src, err := os.Open(self)
	if err != nil {
		return "", err
	}
	defer src.Close()

	// Staged then renamed, so an install over a running copy of the same tool
	// cannot fail with ETXTBSY and cannot leave a half-written binary on
	// $PATH if the copy is interrupted.
	staged := dest + ".new"
	out, err := os.OpenFile(staged, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return "", fmt.Errorf("%w: %s", ErrPermission, prefix)
	}
	if _, err := io.Copy(out, src); err != nil {
		out.Close()
		os.Remove(staged)
		return "", err
	}
	if err := out.Close(); err != nil {
		os.Remove(staged)
		return "", err
	}
	if err := os.Rename(staged, dest); err != nil {
		os.Remove(staged)
		return "", err
	}
	return dest, nil
}

// Uninstall removes prefix/ahub. A missing file is a no-op, not a failure:
// uninstalling twice, or on a machine where the tool was never installed this
// way, is not an error.
func Uninstall(prefix string) (string, bool, error) {
	if prefix == "" {
		prefix = DefaultPrefix()
	}
	dest := filepath.Join(prefix, Tool)
	err := os.Remove(dest)
	if os.IsNotExist(err) {
		return dest, false, nil
	}
	if err != nil {
		return dest, false, fmt.Errorf("%w: %s", ErrPermission, dest)
	}
	return dest, true, nil
}

// OnPath reports whether prefix is on $PATH, so install can say so once
// rather than editing a shell profile behind the user's back.
func OnPath(prefix string) bool {
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		if dir == prefix {
			return true
		}
	}
	return false
}
