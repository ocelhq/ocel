#!/usr/bin/env bash
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

resolve_cloudflare_account() {
  local body status resolved
  local attempt=1
  local delay_ms=1000
  body=$(mktemp)

  while [ "$attempt" -le 5 ]; do
    if status=$(curl -sS -o "$body" -w "%{http_code}" \
      -H "Authorization: Bearer ${CLOUDFLARE_API_TOKEN}" \
      "https://api.cloudflare.com/client/v4/accounts/${CLOUDFLARE_ACCOUNT_ID}"); then
      if [ "$status" = "200" ]; then
        resolved=$(node -e 'const fs=require("node:fs");const r=JSON.parse(fs.readFileSync(process.argv[1],"utf8"));process.stdout.write(r.success?String(r.result.id):"")' "$body")
        rm -f "$body"
        printf '%s' "$resolved"
        return
      fi
      if [ "$status" != "429" ] && [ "$status" -lt 500 ]; then
        echo "Cloudflare account lookup returned HTTP $status" >&2
        rm -f "$body"
        return 1
      fi
    else
      status=network-error
    fi

    if [ "$attempt" -eq 5 ]; then
      echo "Cloudflare account lookup gave up after $attempt attempts (last result: $status)" >&2
      rm -f "$body"
      return 1
    fi

    local jitter_ms=$((RANDOM % 1001))
    local wait_ms=$((delay_ms + jitter_ms))
    if [ "$wait_ms" -gt 8000 ]; then wait_ms=8000; fi
    echo "Cloudflare account lookup returned $status; retrying in $((wait_ms / 1000)).$(printf '%03d' $((wait_ms % 1000)))s" >&2
    sleep "$((wait_ms / 1000)).$(printf '%03d' $((wait_ms % 1000)))"
    delay_ms=$((delay_ms * 2))
    if [ "$delay_ms" -gt 8000 ]; then delay_ms=8000; fi
    attempt=$((attempt + 1))
  done
}

resolved_aws=$(aws sts get-caller-identity --query Account --output text)
if [ "$resolved_aws" != "$EXPECTED_AWS_ACCOUNT_ID" ]; then
  fail "AWS credentials resolve to account $resolved_aws, expected $EXPECTED_AWS_ACCOUNT_ID — refusing to deploy"
fi

if [ "$CLOUDFLARE_ACCOUNT_ID" != "$EXPECTED_CLOUDFLARE_ACCOUNT_ID" ]; then
  fail "CLOUDFLARE_ACCOUNT_ID is $CLOUDFLARE_ACCOUNT_ID, expected $EXPECTED_CLOUDFLARE_ACCOUNT_ID — refusing to deploy"
fi

resolved_cf=$(resolve_cloudflare_account)
if [ "$resolved_cf" != "$EXPECTED_CLOUDFLARE_ACCOUNT_ID" ]; then
  fail "Cloudflare token does not resolve account $EXPECTED_CLOUDFLARE_ACCOUNT_ID (got '${resolved_cf:-none}') — refusing to deploy"
fi

echo "Account guard passed: AWS $resolved_aws, Cloudflare $resolved_cf"
