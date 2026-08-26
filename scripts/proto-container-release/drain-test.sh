#!/usr/bin/env bash
set -euo pipefail

# The one property #588 picked kamal-proxy for: does the flip wait for
# in-flight requests before the old container is stopped? /slow holds a
# request open for 2s; the release happens underneath it.

here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
# shellcheck source=/dev/null
source "$here/lib.sh"
# shellcheck source=/dev/null
source "$here/proxy-${OCEL_PROTO_PROXY:-kamal}.sh"

url=$(proxy_url)

log "drain under ${OCEL_PROTO_PROXY:-kamal}"
proxy_up

deploy() {
    local version=$1 id ref name
    id=$(build "$version")
    ref=$(coordinates "$id")
    transfer "$id" "$ref" >/dev/null 2>&1
    name=$(start_new "$ref")
    health_gate "$name" "$ref"
    printf '%s\n' "$name"
}

old=$(deploy old)
proxy_flip "$old" >/dev/null 2>&1
note "old serving: $(curl -sS --max-time 5 "$url")"

new=$(deploy new)
note "new is gated and idle; nothing left to do but flip"

HOLD_MS=${HOLD_MS:-5000}
inflight=$(mktemp)
curl -sS --max-time 60 -o /dev/null -w '%{http_code}' "${url}slow?ms=$HOLD_MS" >"$inflight" 2>&1 &
slowpid=$!
sleep 1

proxy_flip "$new" >/dev/null 2>&1
note "flip returned; stopping $old now"
box "docker rm -f $old >/dev/null"

wait "$slowpid" && rc=0 || rc=$?
code=$(cat "$inflight")
note "in-flight /slow (held ${HOLD_MS}ms): curl exit=$rc status=$code"

if [ "$rc" -eq 0 ] && [ "$code" = 200 ]; then
    note "VERDICT: the flip drained the in-flight request"
else
    note "VERDICT: the in-flight request was DROPPED by the cutover (status $code)"
fi
