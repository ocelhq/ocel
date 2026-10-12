#!/bin/sh
set -eu

payloads_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
runtime_dir=$(CDPATH= cd -- "$payloads_dir/../../runtime" && pwd)
realtime_dir=$(CDPATH= cd -- "$payloads_dir/../../../realtime" && pwd)
gateplugin_dir=$(CDPATH= cd -- "$payloads_dir/../../gateplugin" && pwd)
dist="$payloads_dir/dist"

rm -rf "$dist"
mkdir -p "$dist"
cp "$runtime_dir/dist/node/serve.mjs" "$dist/serve.mjs"
cp -R "$runtime_dir/dist/next" "$dist/next"

(
  cd "$runtime_dir"
  CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -trimpath -buildvcs=false -ldflags="-s -w" -o "$dist/container-runtime-amd64" ./cmd/container
  CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -trimpath -buildvcs=false -ldflags="-s -w" -o "$dist/envsourcesync-amd64" ./cmd/envsourcesync
  CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -trimpath -buildvcs=false -ldflags="-s -w" -o "$dist/bastion-amd64" ./cmd/bastion
)
(
  cd "$realtime_dir"
  CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -trimpath -buildvcs=false -ldflags="-s -w" -o "$dist/realtime-gateway-amd64" ./cmd/gateway
)
(
  cd "$gateplugin_dir"
  CGO_ENABLED=0 GOOS=wasip1 GOARCH=wasm \
    go build -buildmode=c-shared -trimpath -buildvcs=false -ldflags="-s -w" -o "$dist/previewgate.wasm" ./cmd/plugin
)
chmod 755 "$dist/container-runtime-amd64" "$dist/envsourcesync-amd64" "$dist/bastion-amd64" "$dist/realtime-gateway-amd64"
