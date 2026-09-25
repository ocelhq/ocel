#!/bin/sh
set -eu

host_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
provider_dir=$(CDPATH= cd -- "$host_dir/.." && pwd)
runtime_dir=$(CDPATH= cd -- "$provider_dir/../runtime" && pwd)
dist="$host_dir/dist"

arches="amd64 arm64"

rm -rf "$dist"
mkdir -p "$dist"
for arch in $arches; do
  (
    cd "$provider_dir"
    CGO_ENABLED=0 GOOS=linux GOARCH="$arch" \
      go build -trimpath -buildvcs=false -ldflags="-s -w" -o "$dist/ocel-switchboard-$arch" ./cmd/switchboard
  )
  (
    cd "$runtime_dir"
    CGO_ENABLED=0 GOOS=linux GOARCH="$arch" \
      go build -trimpath -buildvcs=false -ldflags="-s -w" -o "$dist/ocel-live-$arch" ./cmd/live
    CGO_ENABLED=0 GOOS=linux GOARCH="$arch" \
      go build -trimpath -buildvcs=false -ldflags="-s -w" -o "$dist/ocel-runtime-$arch" ./cmd/container
  )
  chmod 755 "$dist/ocel-switchboard-$arch" "$dist/ocel-live-$arch" "$dist/ocel-runtime-$arch"
done
