#!/bin/sh
# Builds cli/platform/dist, the Node platform tree platform.go embeds. Gated on
# a hash of its inputs so a repeat `go generate` is free.
set -eu

platform_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
root=$(CDPATH= cd -- "$platform_dir/../.." && pwd)
dist="$platform_dir/dist"

sha256() {
  if command -v sha256sum >/dev/null 2>&1; then sha256sum; else shasum -a 256; fi
}

inputs() {
  find "$platform_dir/src/builder" "$platform_dir/src/vars-ui" "$root/packages/next-runtime/src" -type f
  find "$root"/workers/*/src -type f
  ls "$root"/workers/*/wrangler.jsonc
  echo "$root/pnpm-lock.yaml"
  echo "$platform_dir/generate.sh"
  echo "$platform_dir/build-platform.mjs"
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

node "$platform_dir/build-platform.mjs"

(
  cd "$root"
  pnpm --filter @ocel/worker-nextjs build
  pnpm --filter @ocel/worker-deployments-store build
  pnpm --filter @ocel/worker-isr-writer build
)
cp "$root/workers/nextjs/dist/index.js" "$dist/workers/next-cloudflare.js"
cp "$root/workers/deployments-store/dist/index.js" "$dist/workers/store-cloudflare.js"
cp "$root/workers/isr-writer/dist/index.js" "$dist/workers/isr-writer-cloudflare.js"

printf '%s' "$stamp" >"$dist/STAMP"
