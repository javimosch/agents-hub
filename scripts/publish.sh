#!/bin/sh
# Builds the release artifact and its version document into a directory, and
# checks them. It uploads nothing: it prints the commands for the bkn host.
#   scripts/publish.sh <out-dir>
set -eu

dir=${1:?usage: scripts/publish.sh <out-dir>}
cd "$(dirname "$0")/.."
fail() { echo "publish.sh: $*" >&2; exit 1; }

# An artifact must correspond to a commit anyone can build.
[ -n "${AHUB_PUBLISH_ALLOW_DIRTY:-}" ] || [ -z "$(git status --porcelain)" ] ||
	fail "the tree has uncommitted changes"

mkdir -p "$dir"
version=$(git describe --tags --always)
# Static, so it starts on any Linux; -trimpath, so it carries no local paths.
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath \
	-ldflags "-s -w -X main.Version=$version" -o "$dir/ahub" .

if grep -qaF -e "$HOME/" -e "$(pwd)" "$dir/ahub"; then
	fail "the binary contains local paths"
fi
if command -v file >/dev/null; then
	file "$dir/ahub" | grep -q 'statically linked' || fail "the binary is not static"
fi

sha=$(sha256sum "$dir/ahub" | cut -d' ' -f1)
printf '{"ok":true,"version":"%.12s","download":"/v1/files/ahub/ahub","sha256":"%s"}\n' \
	"$sha" "$sha" >"$dir/ahub.json"

cat >&2 <<DONE
built $version ($(printf %.12s "$sha")) in $dir
On the bkn host, with BKN_DATA and BKN_FILES_DIR set as the server has them,
delete before putting: an overwrite leaves the old bytes on disk.
  bkn files delete ahub ahub.json && bkn files delete ahub ahub
  bkn files put ahub ./ahub --name ahub
  bkn files put ahub ./ahub.json --name ahub.json
DONE
