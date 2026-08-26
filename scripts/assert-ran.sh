#!/usr/bin/env bash
set -euo pipefail

usage() {
    cat <<'EOF'
usage: scripts/assert-ran.sh <go-test-json> <test-name-prefix>

  Fails when any top-level test named <test-name-prefix>... skipped or
  failed, and when none of them passed.
EOF
    exit 2
}

[ $# -eq 2 ] || usage
report=$1
prefix=$2

if [ ! -f "$report" ]; then
    echo "::error::$report does not exist, so no $prefix run was reported"
    exit 1
fi

named() {
    jq -rR --arg action "$1" --arg prefix "$prefix" \
        'fromjson? | objects | select(.Action == $action and .Test != null and (.Test | startswith($prefix)) and (.Test | contains("/") | not)) | .Test' \
        "$report" | sort -u
}

skipped=$(named skip)
if [ -n "$skipped" ]; then
    echo "::error::$prefix skipped, so nothing was proven against a machine:"
    echo "$skipped"
    exit 1
fi

failed=$(named fail)
if [ -n "$failed" ]; then
    echo "::error::$prefix failed against the machine:"
    echo "$failed"
    exit 1
fi

passed=$(named pass)
if [ -z "$passed" ]; then
    echo "::error::no $prefix test ran at all"
    exit 1
fi

echo "$prefix ran against the VM:"
echo "$passed"
