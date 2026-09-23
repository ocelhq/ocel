#!/bin/sh
set -eu

payloads_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
root=$(CDPATH= cd -- "$payloads_dir/../../../.." && pwd)
runtime_dir=$(CDPATH= cd -- "$payloads_dir/../../runtime" && pwd)
dist="$payloads_dir/dist"

rm -rf "$dist"
mkdir -p "$dist/.bundles"
cp "$root/frameworks/node/runtime/dist/serve.mjs" "$dist/serve.mjs"
cp "$root/frameworks/node/runtime/dist/.bundles/node-runtime.json" "$dist/.bundles/node-runtime.json"

(
  cd "$runtime_dir"
  CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -trimpath -buildvcs=false -ldflags="-s -w" -o "$dist/container-runtime-amd64" ./cmd/container
)
chmod 755 "$dist/container-runtime-amd64"
