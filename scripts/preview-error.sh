#!/usr/bin/env bash
set -euo pipefail

log="${RUNNER_TEMP:-}/preview.log"
if [ ! -f "$log" ]; then
  echo "the run left no log"
  exit 0
fi
sed -E 's/\x1b\[[0-9;]*[A-Za-z]//g' "$log" | { grep -Ev '^[[:space:]]*::' || true; } | tail -n 20
