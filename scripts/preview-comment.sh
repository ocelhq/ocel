#!/usr/bin/env bash
set -euo pipefail

usage() {
  echo "usage: scripts/preview-comment.sh <phase> [result-file]" >&2
  exit 2
}

phase=${1:-}
result=${2:-}
[ -n "$phase" ] || usage

. "$(dirname "${BASH_SOURCE[0]}")/preview-report-body.sh"

body=$(mktemp)
trap 'rm -f "$body"' EXIT

preview_report_body "$phase" "$result" > "$body"

pnpm --filter @console/github --silent run render < "$body"
