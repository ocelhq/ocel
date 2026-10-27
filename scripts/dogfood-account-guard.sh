#!/usr/bin/env bash
# Usage: dogfood-account-guard.sh <run events NDJSON> <expected AWS account id>
set -euo pipefail
events=$1
expected=$2
[ -n "$expected" ] || { echo "no expected account is named" >&2; exit 1; }
reached=$(jq -r 'select(.identity.origin.account != null) | .identity.origin.account' "$events" | sort -u | paste -sd, -)
[ "$reached" = "$expected" ] || { echo "the run reached account ${reached:-none}, not $expected" >&2; exit 1; }
