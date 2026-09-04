#!/usr/bin/env bash
set -euo pipefail

here=$(cd "$(dirname "$0")" && pwd)

usage() {
    cat <<'EOF'
usage: scripts/incus-fanout.sh <lanes.tsv>

  One lane per line: <vm-name> TAB <report-file> TAB <command> [TAB <base>]
  Every lane starts at once as `scripts/incus.sh run <vm-name> -- <command>`,
  cloned from <base> when the lane names one, with the command's stdout in
  <report-file>, its stderr in <report-file>.log and "<exit> <seconds>" in
  <report-file>.status. The host is sampled with vmstat for the duration.
  Exits nonzero when any lane does.
EOF
    exit 2
}

[ $# -eq 1 ] || usage
lanes=$1
[ -s "$lanes" ] || usage

as_admin() {
    if id -nG | grep -qw incus-admin; then
        bash -c "$1"
    else
        sg incus-admin -c "$1"
    fi
}

lane() {
    local name=$1 report=$2 command=$3 base=$4 start=$SECONDS rc=0 from=""
    [ -z "$base" ] || from="--from $base"
    as_admin "$here/incus.sh run $from $name -- $command" > "$report" 2> "$report.log" || rc=$?
    echo "$rc $((SECONDS - start))" > "$report.status"
}

host() {
    echo "host $1:"
    free -m
    df -h / /var/lib/incus/storage-pools/default 2>/dev/null || df -h /
    echo
}

host "before the lanes"
as_admin "$here/incus.sh fetch"

profile=${lanes%.tsv}.vmstat
vmstat -t 15 > "$profile" &
sampler=$!

pids=()
reports=()
while IFS=$'\t' read -r name report command base; do
    [ -n "$name" ] || continue
    lane "$name" "$report" "$command" "$base" &
    pids+=($!)
    reports+=("$report")
done < "$lanes"

for pid in "${pids[@]}"; do
    wait "$pid" || true
done
kill "$sampler" 2>/dev/null || true

host "after the lanes"
echo "vmstat every 15s while the lanes ran:"
cat "$profile"
echo

status=0
printf '%-48s %5s %8s\n' lane exit seconds
while IFS=$'\t' read -r name report _; do
    [ -n "$name" ] || continue
    read -r rc secs < "$report.status"
    printf '%-48s %5s %8s\n' "$name" "$rc" "$secs"
    [ "$rc" -eq 0 ] || status=1
done < "$lanes"

for report in "${reports[@]}"; do
    echo "::group::$report.log"
    cat "$report.log"
    echo "::endgroup::"
done

exit "$status"
