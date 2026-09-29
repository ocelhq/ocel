#!/usr/bin/env bash
set -euo pipefail

fail() {
  echo "::error::$1" >&2
  exit 1
}

require() {
  local name=$1
  if [ -z "${!name:-}" ]; then
    fail "$name is not set, so this guard verified nothing about where the run would deploy.
It compares the session's Cloudflare account against EXPECTED_CLOUDFLARE_ACCOUNT_ID, which CI
supplies from the repository secret E2E_EXPECTED_CLOUDFLARE_ACCOUNT_ID; outside CI nothing
supplies it and this is the only answer it can give. Locally, match CLOUDFLARE_ACCOUNT_ID against
that secret's value by hand before running anything that deploys.
See tests/next-compat/run-suite-prompt.md, Preflight item 2."
  fi
}

require EXPECTED_CLOUDFLARE_ACCOUNT_ID
require CLOUDFLARE_API_TOKEN
require CLOUDFLARE_ACCOUNT_ID

if [ "$CLOUDFLARE_ACCOUNT_ID" != "$EXPECTED_CLOUDFLARE_ACCOUNT_ID" ]; then
  fail "CLOUDFLARE_ACCOUNT_ID is $CLOUDFLARE_ACCOUNT_ID, expected $EXPECTED_CLOUDFLARE_ACCOUNT_ID — refusing to deploy"
fi

resolved_cf=$(
  curl -sS -H "Authorization: Bearer ${CLOUDFLARE_API_TOKEN}" \
    "https://api.cloudflare.com/client/v4/accounts/${CLOUDFLARE_ACCOUNT_ID}" |
    node -e 'let s="";process.stdin.on("data",d=>s+=d).on("end",()=>{const r=JSON.parse(s);process.stdout.write(r.success?String(r.result.id):"")})'
)
if [ "$resolved_cf" != "$EXPECTED_CLOUDFLARE_ACCOUNT_ID" ]; then
  fail "Cloudflare token does not resolve account $EXPECTED_CLOUDFLARE_ACCOUNT_ID (got '${resolved_cf:-none}') — refusing to deploy"
fi

echo "Account guard passed: Cloudflare $resolved_cf"
