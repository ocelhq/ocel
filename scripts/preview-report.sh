#!/usr/bin/env bash
set -euo pipefail

usage() {
  echo "usage: scripts/preview-report.sh <phase> [result-file]" >&2
  exit 2
}

phase=${1:-}
result=${2:-}
[ -n "$phase" ] || usage

if [ -z "${OCEL_GITHUB_APP_URL:-}" ]; then
  echo "no github app url is named, nothing to report to" >&2
  exit 1
fi
if [ -z "${OCEL_REPORT_SECRET:-}" ]; then
  echo "no report secret is named, the app would reject the body" >&2
  exit 1
fi

. "$(dirname "${BASH_SOURCE[0]}")/preview-report-body.sh"

body=$(mktemp)
trap 'rm -f "$body"' EXIT

preview_report_body "$phase" "$result" > "$body"

signature=$(openssl dgst -sha256 -hmac "$OCEL_REPORT_SECRET" "$body" | awk '{print $NF}')

curl -fsS -X POST "$OCEL_GITHUB_APP_URL/api/preview" \
  -H 'content-type: application/json' \
  -H "x-ocel-signature-256: sha256=$signature" \
  --data-binary @"$body"
