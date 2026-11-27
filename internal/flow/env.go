package flow

import (
	"os"
	"slices"
	"strings"
)

// basicEnv is what an ordinary program needs in order to run at all. None of
// it is a credential or points at one.
var basicEnv = []string{
	"PATH", "HOME", "USER", "LOGNAME", "SHELL", "LANG", "LANGUAGE", "TZ", "TMPDIR", "TERM",
	"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME",
}

// childEnv is the environment a permitted command starts with: the basics, the
// locale, and the names this machine's operator listed. Everything else --
// ahub's own token and provider key among them -- stays with ahub. It is never
// nil, which exec would read as "inherit everything".
func childEnv(allow []string) []string {
	kept := []string{}
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(name, "LC_") || slices.Contains(basicEnv, name) || slices.Contains(allow, name) {
			kept = append(kept, entry)
		}
	}
	return kept
}
