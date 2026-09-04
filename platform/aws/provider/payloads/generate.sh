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

mkdir -p "$stage/layer/ocel"
build_lambda ./cmd/membrane/bootstrap "$stage/layer/ocel/bootstrap"
cp -R "$root/platform/aws/membrane/dist/." "$stage/layer/ocel/"
pack "$stage/layer" "$dist/membrane-layer.zip"

mkdir -p "$stage/upload-completer"
build_lambda ./cmd/uploadcompleter "$stage/upload-completer/bootstrap"
pack "$stage/upload-completer" "$dist/upload-completer.zip"

for fn in $functions; do
  cp "$root/platform/aws/functions/$fn/dist/$fn.zip" "$dist/$fn.zip"
done
