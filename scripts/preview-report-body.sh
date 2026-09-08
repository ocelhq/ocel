#!/usr/bin/env bash

preview_report_body() {
  local phase=$1
  local result=${2:-}
  local payload=null

  if [ -n "$result" ]; then
    if [ ! -f "$result" ]; then
      echo "$result does not exist" >&2
      return 1
    fi
    payload=$(cat "$result")
  fi

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
     + (if $error == "" then {} else {error: $error} end)'
}
