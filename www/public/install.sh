#!/bin/sh
set -eu
if (set -o pipefail 2>/dev/null); then
  set -o pipefail
fi

downloads="${OCEL_INSTALL_DOWNLOADS:-https://github.com/ocelhq/ocel/releases/download}"
releases="${OCEL_INSTALL_RELEASES:-https://api.github.com/repos/ocelhq/ocel/releases}"
channel="${OCEL_CHANNEL:-stable}"
destination="$HOME/.local/bin"

fail() {
  echo "install.sh: $1" >&2
  exit 1
}

require() {
  command -v "$1" >/dev/null 2>&1 || fail "$1 is required to install ocel"
}

require curl
require tar

if command -v sha256sum >/dev/null 2>&1; then
  digest() { sha256sum "$1" | cut -d ' ' -f 1; }
elif command -v shasum >/dev/null 2>&1; then
  digest() { shasum -a 256 "$1" | cut -d ' ' -f 1; }
else
  fail "sha256sum or shasum is required to verify the download"
fi

kernel=$(uname -s)
machine=$(uname -m)

case "$kernel" in
  Darwin) os=darwin ;;
  Linux) os=linux ;;
  *) fail "ocel ships no binary for $kernel $machine; install it with: npm install -g @ocel/cli" ;;
esac

case "$machine" in
  x86_64 | amd64) arch=amd64 ;;
  arm64 | aarch64) arch=arm64 ;;
  *) fail "ocel ships no binary for $kernel $machine; install it with: npm install -g @ocel/cli" ;;
esac

case "$channel" in
  stable) listing="$releases/latest" pattern='^v[0-9]*\.[0-9]*\.[0-9]*$' ;;
  next) listing="$releases?per_page=100" pattern='^v[0-9]*\.[0-9]*\.[0-9]*-rc\.[0-9]*$' ;;
  nightly) listing="$releases?per_page=100" pattern='^v[0-9]*\.[0-9]*\.[0-9]*-0\.nightly\.[0-9]*\.g[0-9a-f]*$' ;;
  *) fail "OCEL_CHANNEL is $channel; it takes stable, next or nightly" ;;
esac

version="${OCEL_VERSION:-}"
if [ -z "$version" ]; then
  answer=$(curl -fsSL --retry 3 "$listing") ||
    fail "could not read the $channel releases from $listing; set OCEL_VERSION to install a version without it"
  version=$(printf '%s\n' "$answer" | tr ',{' '\n\n' |
    sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' |
    grep -e "$pattern" | head -n 1) || true
  [ -n "$version" ] || fail "$listing names no $channel release"
fi
version="${version#v}"

archive="ocel_${version}_${os}_${arch}.tar.gz"
work=$(mktemp -d)
staged=""
trap 'rm -rf "$work" ${staged:+"$staged"}' EXIT INT TERM

curl -fsSL --retry 3 -o "$work/$archive" "$downloads/v$version/$archive" ||
  fail "could not download $downloads/v$version/$archive"
curl -fsSL --retry 3 -o "$work/checksums.txt" "$downloads/v$version/checksums.txt" ||
  fail "could not download $downloads/v$version/checksums.txt"

expected=$(sed -n "s/^\([0-9a-f]\{64\}\) \{1,\}$archive\$/\1/p" "$work/checksums.txt")
[ -n "$expected" ] || fail "checksums.txt for v$version names no $archive"
actual=$(digest "$work/$archive")
[ "$expected" = "$actual" ] ||
  fail "checksum mismatch for $archive: expected $expected, downloaded $actual"

tar -xzf "$work/$archive" -C "$work" ocel || fail "$archive holds no ocel binary"
mkdir -p "$destination"
staged="$destination/.ocel.$$"
cp "$work/ocel" "$staged"
chmod 0755 "$staged"
mv -f "$staged" "$destination/ocel"

echo "ocel $version is installed at $destination/ocel"
case ":$PATH:" in
  *":$destination:"*) ;;
  *) echo "add it to your PATH: export PATH=\"$destination:\$PATH\"" ;;
esac
