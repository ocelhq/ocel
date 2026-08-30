#!/bin/sh
set -eu

host_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
provider_dir=$(CDPATH= cd -- "$host_dir/.." && pwd)
dist="$host_dir/dist"

arches="amd64 arm64"

sha256() {
  if command -v sha256sum >/dev/null 2>&1; then sha256sum; else shasum -a 256; fi
}

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

{
  find "$provider_dir/cmd/proxyctl" -type f -name '*.go'
  echo "$provider_dir/go.mod"
  echo "$provider_dir/go.sum"
  echo "$host_dir/generate.sh"
} >"$work/inputs"
LC_ALL=C sort -u "$work/inputs" >"$work/sorted"

: >"$work/digests"
while IFS= read -r f; do
  printf '%s ' "${f#"$provider_dir"/}" >>"$work/digests"
  sha256 <"$f" >>"$work/digests"
done <"$work/sorted"
go env GOVERSION >>"$work/digests"
printf '%s\n' "$arches" >>"$work/digests"

stamp=$(sha256 <"$work/digests" | cut -d' ' -f1)

if [ -f "$dist/STAMP" ] && [ "$(cat "$dist/STAMP")" = "$stamp" ]; then
  exit 0
fi

rm -rf "$dist"
mkdir -p "$dist"
for arch in $arches; do
  (
    cd "$provider_dir"
    CGO_ENABLED=0 GOOS=linux GOARCH="$arch" \
      go build -trimpath -buildvcs=false -ldflags="-s -w" -o "$dist/ocel-proxyctl-$arch" ./cmd/proxyctl
  )
  chmod 755 "$dist/ocel-proxyctl-$arch"
done

printf '%s' "$stamp" >"$dist/STAMP"
