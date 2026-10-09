#!/usr/bin/env bash
# Regenerates every committed derived file and fails when one differs from what
# is in the tree. CI runs it on a clean checkout, so the tree is what is
# committed; `mise run check` runs it before a push.
set -euo pipefail

cd "$(dirname "$0")/../.."

mapfile -t generated < <(jq -r '.generated[]' scripts/check/derived.json)

fingerprint() {
  git ls-files --cached --others --exclude-standard -z -- "${generated[@]}" |
    sort -z |
    xargs -0 -r sha256sum
}

before=$(mktemp)
after=$(mktemp)
trap 'rm -f "$before" "$after"' EXIT

fingerprint >"$before"

node scripts/schema/build.mjs
pnpm gen
go generate -C cli ./node ./internal/skill
node scripts/schema/cli-reference.mjs
cp LICENSE NOTICE sdk/

fingerprint >"$after"

if ! diff -u "$before" "$after"; then
  echo "::error::A derived file differs from what regenerating it writes. Run scripts/check/derived.sh and commit the result."
  git status --short -- "${generated[@]}"
  git --no-pager diff -- "${generated[@]}"
  exit 1
fi
