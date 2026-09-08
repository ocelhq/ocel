#!/usr/bin/env bash
set -euo pipefail

tail -n 20 "${RUNNER_TEMP:-}/preview.log" 2>/dev/null || echo "the run left no log"
