#!/usr/bin/env bash
set -euo pipefail

fail() {
  echo "::error::$1" >&2
  exit 1
}

if [ -z "${EXPECTED_AWS_ACCOUNT_ID:-}" ]; then
  fail "EXPECTED_AWS_ACCOUNT_ID is not set; the account guard cannot verify where this run would publish"
fi

resolved_aws=$(aws sts get-caller-identity --query Account --output text)
if [ "$resolved_aws" != "$EXPECTED_AWS_ACCOUNT_ID" ]; then
  fail "AWS credentials resolve to account $resolved_aws, expected $EXPECTED_AWS_ACCOUNT_ID — refusing to publish"
fi

echo "Account guard passed: AWS $resolved_aws"
