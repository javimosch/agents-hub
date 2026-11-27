package update

import (
	"os"
	"path/filepath"
	"strconv"
	"time"
)

// nudgeEvery is how often a passive check may run. The nudge exists so a
// stale binary eventually mentions itself; it is not a reason to make every
// command depend on the network.
const nudgeEvery = 24 * time.Hour

// stateDir is where the last-checked stamp lives, alongside the profile.
func stateDir() string {
	if p := os.Getenv("AHUB_CONFIG"); p != "" {
		return filepath.Dir(p)
	}
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "."
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "agents-hub")
}

// Nudge checks at most once a day and calls warn when the published artifact
// differs from the running one.
//
// It is silent about every failure. A nudge that cannot reach the network, or
// a machine with no artifact published, must not produce output: this runs
// beside real work, and noise here would be noise on every command.
func Nudge(warn func(string, ...any)) {
	if os.Getenv("AHUB_NO_NUDGE") != "" || os.Getenv("CI") != "" {
		return
	}
	stamp := filepath.Join(stateDir(), "last-update-check")
	if raw, err := os.ReadFile(stamp); err == nil {
		if unix, err := strconv.ParseInt(string(raw), 10, 64); err == nil {
			if time.Since(time.Unix(unix, 0)) < nudgeEvery {
				return
			}
		}
	}
	_ = os.MkdirAll(stateDir(), 0o700)
	_ = os.WriteFile(stamp, []byte(strconv.FormatInt(time.Now().Unix(), 10)), 0o600)

	rel, err := Fetch(3 * time.Second)
	if err != nil {
		return
	}
	local, err := LocalVersion()
	if err != nil || rel.Version == local {
		return
	}
	warn("[update] a newer ahub is available (%s → %s). Run: ahub update", local, rel.Version)
}
