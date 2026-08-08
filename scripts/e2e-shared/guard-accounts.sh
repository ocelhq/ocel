#!/usr/bin/env bash
# Refuses to let a deployment-e2e harness (scripts/e2e-next, scripts/e2e-node)
# deploy into anything but the disposable account it is scoped to.
#
# A suite creates and destroys many Lambdas, worker scripts and DNS labels
# under a shared project. A mistyped or rotated secret pointing at a real
# account would spray that into production infrastructure, so the resolved
# identities are compared against the expected ones before any deploy — in the
# build job and again in every job that deploys.
set -euo pipefail

fail() {
  echo "::error::$1" >&2
  exit 1
}

require() {
  local name=$1
  if [ -z "${!name:-}" ]; then
    fail "$name is not set; the account guard cannot verify where this run would deploy"
  fi
}

require EXPECTED_AWS_ACCOUNT_ID
require EXPECTED_CLOUDFLARE_ACCOUNT_ID
require CLOUDFLARE_API_TOKEN
require CLOUDFLARE_ACCOUNT_ID

resolved_aws=$(aws sts get-caller-identity --query Account --output text)
if [ "$resolved_aws" != "$EXPECTED_AWS_ACCOUNT_ID" ]; then
  fail "AWS credentials resolve to account $resolved_aws, expected $EXPECTED_AWS_ACCOUNT_ID — refusing to deploy"
fi

if [ "$CLOUDFLARE_ACCOUNT_ID" != "$EXPECTED_CLOUDFLARE_ACCOUNT_ID" ]; then
  fail "CLOUDFLARE_ACCOUNT_ID is $CLOUDFLARE_ACCOUNT_ID, expected $EXPECTED_CLOUDFLARE_ACCOUNT_ID — refusing to deploy"
fi

# Confirm the token actually holds that account, rather than trusting the id
# variable: a token for a different account would otherwise pass the check above
# and then create workers somewhere else entirely.
resolved_cf=$(
  curl -sS -H "Authorization: Bearer ${CLOUDFLARE_API_TOKEN}" \
    "https://api.cloudflare.com/client/v4/accounts/${CLOUDFLARE_ACCOUNT_ID}" |
    node -e 'let s="";process.stdin.on("data",d=>s+=d).on("end",()=>{const r=JSON.parse(s);process.stdout.write(r.success?String(r.result.id):"")})'
)
if [ "$resolved_cf" != "$EXPECTED_CLOUDFLARE_ACCOUNT_ID" ]; then
  fail "Cloudflare token does not resolve account $EXPECTED_CLOUDFLARE_ACCOUNT_ID (got '${resolved_cf:-none}') — refusing to deploy"
fi

echo "Account guard passed: AWS $resolved_aws, Cloudflare $resolved_cf"
