#!/bin/sh
# Fetches the published ahub, verifies it, and lets it install itself.
#   AHUB_SERVER      where it is published (default https://bkn.intrane.fr)
#   AHUB_UPDATE_URL  the version document, if not at the default path
#   AHUB_PREFIX      install directory (default ~/.local/bin)
set -eu

server=${AHUB_SERVER:-https://bkn.intrane.fr}
server=${server%/}
manifest=${AHUB_UPDATE_URL:-$server/v1/files/ahub/ahub.json}

fail() { echo "install.sh: $*" >&2; exit 1; }

# The published artifact is this one platform. Elsewhere, build from source.
[ "$(uname -s)-$(uname -m)" = "Linux-x86_64" ] ||
	fail "published builds are Linux x86_64 only; build from source with: go build ."

tmp=$(mktemp -d)
trap 'rm -f "$tmp/ahub" "$tmp/ahub.json"; rmdir "$tmp"' EXIT

curl -fsS -m 30 -o "$tmp/ahub.json" "$manifest" || fail "cannot read $manifest"
field() { sed -n "s/.*\"$1\"[[:space:]]*:[[:space:]]*\"\([^\"]*\)\".*/\1/p" "$tmp/ahub.json"; }
version=$(field version)
sha=$(field sha256)
download=$(field download)
[ -n "$version" ] && [ -n "$download" ] || fail "$manifest is not a version document"
case $download in
	http://* | https://*) ;;
	*) download=$server/${download#/} ;;
esac

curl -fsS -m 300 -o "$tmp/ahub" "$download" || fail "cannot download $download"
got=$(sha256sum "$tmp/ahub" | cut -d' ' -f1)
[ "$(printf %.12s "$got")" = "$version" ] || fail "hash mismatch: nothing was installed"
[ -z "$sha" ] || [ "$got" = "$sha" ] || fail "hash mismatch: nothing was installed"

chmod +x "$tmp/ahub"
"$tmp/ahub" version | grep -q '"tool":"ahub"' || fail "the download does not run as ahub"

if [ -n "${AHUB_PREFIX:-}" ]; then
	"$tmp/ahub" install --prefix "$AHUB_PREFIX"
else
	"$tmp/ahub" install
fi
echo "Run 'ahub update' to stay current." >&2
