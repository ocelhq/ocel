#!/usr/bin/env bash
set -euo pipefail

here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
# shellcheck source=/dev/null
source "$here/lib.sh"
# shellcheck source=/dev/null
source "$here/proxy-${OCEL_PROTO_PROXY:-kamal}.sh"

SURPRISES=$(mktemp)
surprise() {
    printf -- '- %s\n' "$*" >>"$SURPRISES"
    note "SURPRISE: $*"
}

serving() { curl -sS --max-time 5 "$(proxy_url)"; }

RELEASED=

release() {
    local version=$1 id ref name before after
    RELEASED=
    {
        log "release $version"
        id=$(build "$version")
        ref=$(coordinates "$id")
        note "coordinates: $ref (from image id $id)"
        transfer "$id" "$ref"
        name=$(start_new "$ref")
        health_gate "$name" "$ref" || return 1
        before=$(running_containers | grep -v "^$name\$" || true)
        if ! proxy_flip "$name"; then
            note "flip refused $name; the old container keeps serving"
            box "docker rm -f $name >/dev/null 2>&1 || true"
            return 1
        fi
        for old in $before; do
            note "stopping drained $old"
            box "docker rm -f $old >/dev/null"
        done
        after=$(running_containers | tr '\n' ' ')
        note "now running: $after"
        record "$ref"
    } >&2
    RELEASED=$ref
}

expect() {
    local what=$1 want=$2 got=$3
    if [ "$want" = "$got" ]; then
        note "ok: $what = $got"
    else
        surprise "$what: wanted '$want', got '$got'"
    fi
}

log "proxy: ${OCEL_PROTO_PROXY:-kamal}"
proxy_up

release v1
v1=$RELEASED
expect "proxy serves v1" v1 "$(serving)"

log "release v2 under continuous load"
drops=$(mktemp)
(
    fails=0 total=0
    end=$((SECONDS + 60))
    while [ "$SECONDS" -lt "$end" ] && [ ! -f "$drops.stop" ]; do
        total=$((total + 1))
        curl -sS -o /dev/null --max-time 5 "$(proxy_url)" || fails=$((fails + 1))
    done
    printf '%s %s\n' "$fails" "$total" >"$drops"
) &
loadpid=$!
release v2
v2=$RELEASED
touch "$drops.stop"
wait "$loadpid" || true
read -r fails total <"$drops"
note "load across the cutover: $fails failed of $total"
expect "dropped requests across the flip" 0 "$fails"
expect "proxy serves v2" v2 "$(serving)"

log "a release that never listens must not take traffic"
if BOOT_DELAY_MS=600000 release v3; then
    surprise "a container that never listened passed the health gate"
else
    note "ok: gate refused the dead release"
fi
expect "proxy still serves v2 after the failed gate" v2 "$(serving)"
expect "no two-container end state" 1 "$(running_containers | grep -c .)"

log "an app that 404s its root: #586 says up, the proxy may disagree"
if ROOT_STATUS=404 release v404; then
    note "ok: a 404 root still took traffic"
    expect "proxy serves the 404-root release" v404 "$(serving)"
    release v2b
    v2=$RELEASED
else
    surprise "the proxy refused a release that #586 calls up: an app that 404s its root cannot deploy"
    box "docker rm -f $(container_of "${RELEASED:-none}") >/dev/null 2>&1 || true"
fi

log "rollback: re-run the retained v1 digest"
name=$(container_of "$v1")
box "docker rm -f $name >/dev/null 2>&1 || true"
if box "docker image inspect $v1 >/dev/null 2>&1"; then
    note "ok: box retained $v1, no re-transfer needed"
else
    surprise "box did not retain $v1, rollback would need a re-transfer"
fi
name=$(start_new "$v1")
health_gate "$name" "$v1"
proxy_flip "$name"
box "docker rm -f $(container_of "$v2") >/dev/null 2>&1 || true"
expect "proxy serves v1 after rollback" v1 "$(serving)"

log "retention: the box keeps the last $KEEP digests"
release v4
release v5
retain
expect "ledger length" "$KEEP" "$(box "wc -l < $LEDGER" | tr -d ' ')"
expect "images on the box match the ledger" \
    "$(box "tail -n $KEEP $LEDGER" | sort | tr '\n' ' ')" \
    "$(held | sort | tr '\n' ' ')"

log "what surprised"
if [ -s "$SURPRISES" ]; then
    cat "$SURPRISES"
    exit 1
fi
note "nothing surprised"
