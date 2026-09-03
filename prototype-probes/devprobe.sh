#!/bin/bash
root=/home/vndaba/Dev/ocelhq/.claude/worktrees/prototype+example-config-layout
xdg=/home/vndaba/.claude/jobs/7d858f85/tmp/xdg
mkdir -p "$xdg/ocel"
printf '{"access_token":"stub","api_url":"http://127.0.0.1:9"}' > "$xdg/ocel/credentials.json"
export XDG_CONFIG_HOME="$xdg"
cd "$root/examples/$1" || exit 99
shift
echo "=== $(basename "$PWD"): ocel dev $*"
timeout 30 "$root/cli/bin/ocel" dev "$@" </dev/null 2>&1 | head -20
echo "exit=${PIPESTATUS[0]}"
