#!/bin/sh
# Installs Libre Publish: one file, for this machine, from the latest
# release (or LIBREPUBLISH_VERSION=v2.0.0), checked against the release's
# checksums.
#
#   curl -fsSL https://raw.githubusercontent.com/Martin-Business-Consultants/librepublishgo/main/install.sh | sh
#
# main moves: pin the script to a release tag (…/librepublishgo/v2.7.0/
# install.sh) to run the one you read.
#
# It goes to /usr/local/bin when that's writable, else ~/.local/bin
# (LIBREPUBLISH_DIR=… to choose).
set -eu

repo="Martin-Business-Consultants/librepublishgo"
version="${LIBREPUBLISH_VERSION:-latest}"

case "$(uname -s)" in
  Linux) os=linux ;;
  Darwin) os=darwin ;;
  *) echo "librepublish: no build for $(uname -s); on Windows, download librepublish-windows-amd64.exe from https://github.com/$repo/releases" >&2; exit 1 ;;
esac
case "$(uname -m)" in
  x86_64 | amd64) arch=amd64 ;;
  arm64 | aarch64) arch=arm64 ;;
  *) echo "librepublish: no build for $(uname -m)" >&2; exit 1 ;;
esac

file="librepublish-$os-$arch"

# Over HTTPS only, redirects included: what's fetched is what runs.
fetch() { curl -fsSL --proto '=https' --proto-redir '=https' --tlsv1.2 "$@"; }

# The repo's releases include the Rails core's (from main), which carry no
# binary, so "latest" is the newest release that has this file.
if [ -n "${LIBREPUBLISH_BASE_URL:-}" ]; then
  base="$LIBREPUBLISH_BASE_URL" # a mirror, or a local dist/ to test with
  case "$base" in
    https://*) ;;
    file://*) fetch() { curl -fsSL "$@"; } ;; # local files: nothing in transit
    *) echo "librepublish: LIBREPUBLISH_BASE_URL must be https:// (or file:// for a local dist/), not $base" >&2; exit 1 ;;
  esac
elif [ "$version" = latest ]; then
  version="$(fetch "https://api.github.com/repos/$repo/releases?per_page=100" |
    tr ',' '\n' | grep -E '"(tag_name|browser_download_url)"' |
    awk -v f="/$file\"" '/tag_name/ { tag = $0 } index($0, f) { print tag; exit }' |
    sed 's/.*"tag_name": *"\([^"]*\)".*/\1/')"
  if [ -z "$version" ]; then
    echo "librepublish: no release has a $file build yet." >&2
    echo "From a checkout with Go installed, run it directly instead: go run ./cmd/librepublish serve" >&2
    exit 1
  fi
  base="https://github.com/$repo/releases/download/$version"
else
  base="https://github.com/$repo/releases/download/$version"
fi
if [ -n "${LIBREPUBLISH_DIR:-}" ]; then
  dir="$LIBREPUBLISH_DIR"
elif [ -w /usr/local/bin ]; then
  dir=/usr/local/bin
else
  dir="$HOME/.local/bin"
fi
mkdir -p "$dir"

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
echo "Downloading $file ($version)…"
fetch "$base/$file" -o "$tmp/$file"
fetch "$base/checksums.txt" -o "$tmp/checksums.txt"

# Exactly one line for the file, and a whole SHA-256 on it.
expected="$(awk -v f="$file" '$2 == f || $2 == "*" f { print $1 }' "$tmp/checksums.txt")"
case "$expected" in
  *[!0-9a-f]* | "") expected="" ;;
esac
[ "${#expected}" = 64 ] || expected=""
if command -v sha256sum >/dev/null 2>&1; then
  actual="$(sha256sum "$tmp/$file" | cut -d' ' -f1)"
else
  actual="$(shasum -a 256 "$tmp/$file" | cut -d' ' -f1)"
fi
if [ -z "$expected" ] || [ "$expected" != "$actual" ]; then
  echo "librepublish: the download doesn't match the release's checksum; not installing it." >&2
  exit 1
fi

chmod +x "$tmp/$file"
mv "$tmp/$file" "$dir/librepublish"
echo "Installed $("$dir/librepublish" version) at $dir/librepublish"
case ":$PATH:" in *":$dir:"*) ;; *) echo "Add $dir to your PATH to run it as \`librepublish\`." ;; esac
echo
echo "Start it:  librepublish serve --data ./data"
echo "Then open http://localhost:8080/ and make the first administrator."
