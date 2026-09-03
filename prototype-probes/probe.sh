#!/bin/bash
# usage: probe.sh <example-dir> <ocel subcommand...>
root=/home/vndaba/Dev/ocelhq/.claude/worktrees/prototype+example-config-layout
export OCEL_VPS_HOST=h OCEL_VPS_USER=u OCEL_VPS_IDENTITY_FILE=/tmp/nope
cd "$root/examples/$1" || exit 99
shift
for c in ocel.config.ts ocel.aws.config.ts ocel.vps.config.ts; do
  echo "=== $(basename "$PWD"): ocel $* --config $c"
  timeout 90 "$root/cli/bin/ocel" "$@" --config "$c" 2>&1 | head -40
  echo "exit=${PIPESTATUS[0]}"
done
