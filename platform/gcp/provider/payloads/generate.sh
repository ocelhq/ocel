#!/bin/sh
set -eu

payloads_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
root=$(CDPATH= cd -- "$payloads_dir/../../../.." && pwd)
dist="$payloads_dir/dist"

rm -rf "$dist"
mkdir -p "$dist"
cp "$root/frameworks/node/runtime/dist/serve.mjs" "$dist/serve.mjs"
