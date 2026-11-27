package main

import (
	"errors"
	"os"
	"time"

	"github.com/javimosch/agents-hub/internal/out"
	"github.com/javimosch/agents-hub/internal/update"
)

// updateCmd implements cli-update-spec's update command: check, verify, swap.
func updateCmd(fl *flags) {
	local, err := update.LocalVersion()
	if err != nil {
		out.Fail(out.InternalError, "internal_error", err.Error())
	}
	if fl.has("check") {
		rel, err := update.Fetch(20 * time.Second)
		if err != nil {
			failUpdate(err)
		}
		out.Data(map[string]any{
			"current": local, "latest": rel.Version,
			"update_available": rel.Version != local,
			"source":           update.VersionURL(),
		})
		if rel.Version != local {
			// cli-update-spec §3: not an error, so the data is on stdout.
			os.Exit(exitUpdateAvailable)
		}
		return
	}
	res, err := update.Apply(fl.has("force"), out.Log)
	if err != nil {
		failUpdate(err)
	}
	out.Data(map[string]any{
		"updated": res.Updated, "from": res.From, "to": res.To,
		"path": res.Path, "backup": res.Backup,
	})
}

// exitUpdateAvailable is what `update --check` exits when there is one.
const exitUpdateAvailable = 5

// failUpdate maps an update failure onto cli-update-spec's exit codes: any
// failure to download, verify, run or swap is 100.
func failUpdate(err error) {
	switch {
	case errors.Is(err, update.ErrNoArtifact):
		out.Fail(out.NotFound, "no_artifact", err.Error(),
			"check AHUB_SERVER, or AHUB_UPDATE_URL for a non-default location")
	case errors.Is(err, update.ErrInProgress):
		out.Fail(out.Conflict, "update_in_progress", err.Error(), "wait for it to finish")
	case errors.Is(err, update.ErrVerify):
		out.Fail(out.ConnectionError, "verify_failed", err.Error(),
			"the download does not match its advertised hash; if it repeats, the publish is broken")
	case errors.Is(err, update.ErrSmokeTest):
		out.Fail(out.ConnectionError, "smoke_test_failed", err.Error(),
			"the published binary is not a working ahub; the installed one is untouched")
	case errors.Is(err, update.ErrPermission):
		out.Fail(out.NotAuthenticated, "permission_denied", err.Error())
	default:
		out.Fail(out.ConnectionError, "update_failed", err.Error())
	}
}

// installCmd copies the running binary somewhere stable on $PATH.
func installCmd(fl *flags) {
	prefix := fl.str("prefix")
	dest, err := update.Install(prefix)
	if err != nil {
		if errors.Is(err, update.ErrPermission) {
			out.Fail(out.NotAuthenticated, "permission_denied", err.Error(),
				"choose a writable --prefix; this never uses sudo on your behalf")
		}
		out.Fail(out.InternalError, "internal_error", err.Error())
	}
	dir := prefix
	if dir == "" {
		dir = update.DefaultPrefix()
	}
	if !update.OnPath(dir) {
		// Said once, on stderr, and nothing is edited: a tool that rewrites a
		// shell profile to make itself work is a tool you cannot uninstall.
		out.Log("%s is not on $PATH; add it, or call %s directly", dir, dest)
	}
	out.Data(map[string]any{"ok": true, "installed": dest})
}

func uninstallCmd(fl *flags) {
	dest, removed, err := update.Uninstall(fl.str("prefix"))
	if err != nil {
		out.Fail(out.NotAuthenticated, "permission_denied", err.Error())
	}
	// The config directory is left alone: deleting a profile, its token and
	// its secrets must never be a side effect of removing a binary.
	out.Data(map[string]any{"ok": true, "removed": removed, "path": dest})
}
