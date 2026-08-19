#!/bin/sh
set -eu

command -v zip >/dev/null || {
  echo "zip is required to build the provider payloads" >&2
  exit 1
}

payloads_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
provider_dir=$(CDPATH= cd -- "$payloads_dir/.." && pwd)
root=$(CDPATH= cd -- "$provider_dir/../../.." && pwd)
dist="$payloads_dir/dist"

functions="image-optimizer revalidator tag-publisher tag-invalidator"

work=$(mktemp -d)
stage="$work/stage"
mkdir -p "$stage"
trap 'rm -rf "$work"' EXIT

sha256() {
  if command -v sha256sum >/dev/null 2>&1; then sha256sum; else shasum -a 256; fi
}

tree() {
  if [ ! -d "$1" ]; then
    echo "generate.sh: ${1#"$root"/} is not a directory" >&2
    exit 1
  fi
  find "$1" -type f
}

go_sources() {
  (
    cd "$provider_dir"
    CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
      go list -deps -tags lambda.norpc -f '{{.Dir}}' ./cmd/membrane/bootstrap ./cmd/uploadcompleter
  ) >"$work/godirs"
  while IFS= read -r dir; do
    case "$dir" in
    "$root"/*) find "$dir" -maxdepth 1 -type f -name '*.go' ;;
    esac
  done <"$work/godirs"
}

inputs() {
  tree "$root/platform/aws/membrane/src"
  tree "$root/platform/aws/membrane/scripts"
  echo "$root/platform/aws/membrane/package.json"
  echo "$root/platform/aws/membrane/tsconfig.json"
  for fn in $functions; do
    tree "$root/platform/aws/functions/$fn/src"
    tree "$root/platform/aws/functions/$fn/scripts"
    echo "$root/platform/aws/functions/$fn/package.json"
    echo "$root/platform/aws/functions/$fn/tsconfig.json"
  done
  tree "$root/frameworks/next/router/src"
  tree "$root/frameworks/next/cache/src"
  tree "$root/frameworks/next/protocol/src"
  tree "$root/platform/edge/contract/src"
  go_sources
  echo "$provider_dir/go.mod"
  echo "$provider_dir/go.sum"
  echo "$root/pnpm-lock.yaml"
  echo "$payloads_dir/generate.sh"
}

inputs >"$work/inputs"
LC_ALL=C sort -u "$work/inputs" >"$work/sorted"

: >"$work/digests"
while IFS= read -r f; do
  printf '%s ' "${f#"$root"/}" >>"$work/digests"
  sha256 <"$f" >>"$work/digests"
done <"$work/sorted"
go env GOVERSION >>"$work/digests"

stamp=$(sha256 <"$work/digests" | cut -d' ' -f1)

if [ -f "$dist/STAMP" ] && [ "$(cat "$dist/STAMP")" = "$stamp" ]; then
  exit 0
fi

pack() {
  chmod -R u=rwX,go=rX "$1"
  find "$1" -exec touch -t 198001010000 {} +
  rm -f "$2"
  (cd "$1" && find . -mindepth 1 | LC_ALL=C sort | zip -X -q -@ "$2")
}

build_lambda() {
  (
    cd "$provider_dir"
    CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
      go build -trimpath -buildvcs=false -tags lambda.norpc -ldflags="-s -w" -o "$2" "$1"
  )
  chmod 755 "$2"
}

rm -rf "$dist"
mkdir -p "$dist"

rm -rf "$root/platform/aws/membrane/dist"
(
  cd "$root"
  pnpm --filter @platform/aws-membrane build
)
mkdir -p "$stage/layer/ocel"
build_lambda ./cmd/membrane/bootstrap "$stage/layer/ocel/bootstrap"
cp -R "$root/platform/aws/membrane/dist/." "$stage/layer/ocel/"
pack "$stage/layer" "$dist/membrane-layer.zip"

mkdir -p "$stage/upload-completer"
build_lambda ./cmd/uploadcompleter "$stage/upload-completer/bootstrap"
pack "$stage/upload-completer" "$dist/upload-completer.zip"

for fn in $functions; do
  (
    cd "$root"
    pnpm --filter "@platform/aws-$fn" zip
  )
  cp "$root/platform/aws/functions/$fn/dist/$fn.zip" "$dist/$fn.zip"
done

printf '%s' "$stamp" >"$dist/STAMP"
