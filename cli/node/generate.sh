#!/bin/sh
set -eu

node_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
root=$(CDPATH= cd -- "$node_dir/../.." && pwd)
dist="$node_dir/dist"

sha256() {
  if command -v sha256sum >/dev/null 2>&1; then sha256sum; else shasum -a 256; fi
}

inputs() {
  find "$node_dir/src/builder" "$node_dir/src/vars-ui" "$root/frameworks/next/adapter/src" -type f
  find "$root"/platform/edge/cloudflare/workers/*/src -type f
  ls "$root"/platform/edge/cloudflare/workers/*/wrangler.jsonc
  echo "$root/pnpm-lock.yaml"
  echo "$node_dir/generate.sh"
  echo "$node_dir/build.mjs"
}

stamp=$(
  inputs | LC_ALL=C sort | while IFS= read -r f; do
    printf '%s\n' "${f#"$root"/}"
    cat "$f"
  done | sha256 | cut -d' ' -f1
)

if [ -f "$dist/STAMP" ] && [ "$(cat "$dist/STAMP")" = "$stamp" ]; then
  exit 0
fi

node "$node_dir/build.mjs"

(
  cd "$root"
  pnpm --filter @platform/cf-entry build
  pnpm --filter @platform/cf-deployments-store build
  pnpm --filter @platform/cf-isr-writer build
)
cp "$root/platform/edge/cloudflare/workers/entry/dist/index.js" "$dist/workers/entry-cloudflare.js"
cp "$root/platform/edge/cloudflare/workers/deployments-store/dist/index.js" "$dist/workers/store-cloudflare.js"
cp "$root/platform/edge/cloudflare/workers/isr-writer/dist/index.js" "$dist/workers/isr-writer-cloudflare.js"

printf '%s' "$stamp" >"$dist/STAMP"
