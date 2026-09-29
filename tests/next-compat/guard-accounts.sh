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
It compares the session's accounts against EXPECTED_AWS_ACCOUNT_ID and EXPECTED_CLOUDFLARE_ACCOUNT_ID,
which CI supplies from the repository secrets E2E_EXPECTED_AWS_ACCOUNT_ID and
E2E_EXPECTED_CLOUDFLARE_ACCOUNT_ID; outside CI nothing supplies them and this is the only answer it
can give. Locally, resolve the accounts yourself and match them by hand before running anything that
deploys — \`aws sts get-caller-identity\` and CLOUDFLARE_ACCOUNT_ID, against those two secrets' values.
See tests/next-compat/run-suite-prompt.md, Preflight item 2."
  fi
}

require EXPECTED_AWS_ACCOUNT_ID

resolved_aws=$(aws sts get-caller-identity --query Account --output text)
if [ "$resolved_aws" != "$EXPECTED_AWS_ACCOUNT_ID" ]; then
  fail "AWS credentials resolve to account $resolved_aws, expected $EXPECTED_AWS_ACCOUNT_ID — refusing to deploy"
fi

"$(dirname "$0")/guard-cloudflare.sh"

echo "Account guard passed: AWS $resolved_aws"
