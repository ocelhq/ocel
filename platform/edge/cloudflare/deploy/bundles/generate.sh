#!/bin/sh
set -eu

bundles_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
workers=$(CDPATH= cd -- "$bundles_dir/../../workers" && pwd)
dist="$bundles_dir/dist"

rm -rf "$dist"
mkdir -p "$dist"
for worker in entry deployments-store isr-writer refresher; do
  cp "$workers/$worker/dist/index.js" "$dist/$worker.js"
done
