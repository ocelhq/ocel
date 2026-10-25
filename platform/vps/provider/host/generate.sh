#!/bin/sh
set -eu

host_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
provider_dir=$(CDPATH= cd -- "$host_dir/.." && pwd)
box_dir=$(CDPATH= cd -- "$provider_dir/../box" && pwd)
realtime_dir=$(CDPATH= cd -- "$provider_dir/../../realtime" && pwd)
runtime_dir=$(CDPATH= cd -- "$provider_dir/../runtime" && pwd)
dist="$host_dir/dist"

arches="amd64 arm64"

rm -rf "$dist"
mkdir -p "$dist/next-container"
cp "$runtime_dir/dist/next/entrypoint.mjs" "$dist/next-container/entrypoint.mjs"
mkdir -p "$dist/next-container/sharp"
cp -R "$runtime_dir/dist/sharp/x64" "$dist/next-container/sharp/amd64"
cp -R "$runtime_dir/dist/sharp/arm64" "$dist/next-container/sharp/arm64"
for arch in $arches; do
  (
    cd "$box_dir"
    CGO_ENABLED=0 GOOS=linux GOARCH="$arch" \
      go build -trimpath -buildvcs=false -ldflags="-s -w" -o "$dist/ocel-switchboard-$arch" ./cmd/switchboard
    CGO_ENABLED=0 GOOS=linux GOARCH="$arch" \
      go build -trimpath -buildvcs=false -ldflags="-s -w" -o "$dist/ocel-live-$arch" ./cmd/live
    CGO_ENABLED=0 GOOS=linux GOARCH="$arch" \
      go build -trimpath -buildvcs=false -ldflags="-s -w" -o "$dist/ocel-runtime-$arch" ./cmd/container
  )
  (
    cd "$realtime_dir"
    CGO_ENABLED=0 GOOS=linux GOARCH="$arch" \
      go build -trimpath -buildvcs=false -ldflags="-s -w" -o "$dist/ocel-realtime-$arch" ./cmd/gateway
  )
  chmod 755 "$dist/ocel-switchboard-$arch" "$dist/ocel-live-$arch" "$dist/ocel-runtime-$arch" "$dist/ocel-realtime-$arch"
done
