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

payload=null
if [ -n "$result" ]; then
  if [ ! -f "$result" ]; then
    echo "$result does not exist" >&2
    exit 1
  fi
  payload=$(cat "$result")
fi

body=$(mktemp)
trap 'rm -f "$body"' EXIT

jq -n \
  --arg repo "${GITHUB_REPOSITORY:-}" \
  --argjson pr "${PR:-0}" \
  --arg sha "${SHA:-}" \
  --arg ref "${REF:-}" \
  --arg run_url "${RUN_URL:-}" \
  --arg phase "$phase" \
  --arg error "${ERROR:-}" \
  --argjson result "$payload" \
  '{repo: $repo, pr: $pr, sha: $sha, ref: $ref, run_url: $run_url, phase: $phase}
   + (if $result == null then {} else {result: $result} end)
   + (if $error == "" then {} else {error: $error} end)' > "$body"

signature=$(openssl dgst -sha256 -hmac "$OCEL_REPORT_SECRET" "$body" | awk '{print $NF}')

curl -fsS -X POST "$OCEL_GITHUB_APP_URL/api/preview" \
  -H 'content-type: application/json' \
  -H "x-ocel-signature-256: sha256=$signature" \
  --data-binary @"$body"
