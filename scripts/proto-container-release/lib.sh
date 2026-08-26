#!/usr/bin/env bash

APP=hello
REPO="ocel/$APP"
NET=ocel
PORT=8080
KEEP=3

: "${OCEL_INCUS_ADDR:?box address}"
: "${OCEL_INCUS_USER:?box user}"
: "${OCEL_INCUS_KEY:?box key}"

log() { printf '\n=== %s\n' "$*" >&2; }
note() { printf '    %s\n' "$*" >&2; }

box() {
    ssh -i "$OCEL_INCUS_KEY" \
        -o IdentitiesOnly=yes \
        -o BatchMode=yes \
        -o StrictHostKeyChecking=no \
        -o UserKnownHostsFile=/dev/null \
        -o LogLevel=ERROR \
        "$OCEL_INCUS_USER@$OCEL_INCUS_ADDR" "$@"
}

build() {
    local version=$1
    docker build -q --build-arg "APP_VERSION=$version" -t "$REPO:build-$version" \
        "$(dirname "${BASH_SOURCE[0]}")/app" >/dev/null
    docker image inspect -f '{{.Id}}' "$REPO:build-$version"
}

coordinates() {
    local hex=${1#sha256:}
    printf '%s:sha256-%s\n' "$REPO" "${hex:0:12}"
}

transfer() {
    local id=$1 ref=$2
    if box "docker image inspect $id >/dev/null 2>&1"; then
        note "dedup: box already holds $id, no transfer"
        box "docker tag $id $ref"
        return
    fi
    docker tag "$id" "$ref"
    note "streaming $ref"
    docker save "$ref" | box "docker load"
}

container_of() { printf '%s-%s\n' "$APP" "${1##*:}"; }

start_new() {
    local ref=$1 name
    name=$(container_of "$ref")
    box "docker rm -f $name >/dev/null 2>&1 || true"
    box "docker run -d --name $name --network $NET --restart unless-stopped \
        --label ocel.app=$APP --label ocel.ref=$ref \
        -e PORT=$PORT -e BOOT_DELAY_MS=${BOOT_DELAY_MS:-0} \
        -e ROOT_STATUS=${ROOT_STATUS:-200} $ref" >/dev/null
    printf '%s\n' "$name"
}

health_gate() {
    local name=$1 ref=$2 deadline=$((SECONDS + 30))
    while [ "$SECONDS" -lt "$deadline" ]; do
        if box "docker run --rm --network $NET $ref node -e \
            'require(\"http\").get({host:\"$name\",port:$PORT},r=>process.exit(0)).on(\"error\",()=>process.exit(1))'" \
            >/dev/null 2>&1; then
            note "gate passed: $name answered HTTP"
            return 0
        fi
        sleep 1
    done
    note "gate FAILED for $name, logs follow:"
    box "docker logs $name 2>&1 | tail -20" >&2 || true
    box "docker rm -f $name >/dev/null 2>&1 || true"
    return 1
}

running_containers() {
    box "docker ps --filter label=ocel.app=$APP --format '{{.Names}}'"
}

LEDGER=/var/lib/ocel/releases

record() {
    box "mkdir -p $(dirname $LEDGER) && sed -i '\\|^$1\$|d' $LEDGER 2>/dev/null; echo $1 >> $LEDGER"
}

retain() {
    local live
    live=$(box "docker ps --filter label=ocel.app=$APP --format '{{.Label \"ocel.ref\"}}'")
    note "live ref: $live"
    for ref in $(box "head -n -$KEEP $LEDGER 2>/dev/null || true"); do
        if [ "$ref" = "$live" ]; then
            surprise "retention wanted to prune the running ref $ref"
            continue
        fi
        note "pruning $ref"
        box "docker rmi $ref >/dev/null 2>&1 || true"
    done
    box "tail -n $KEEP $LEDGER > $LEDGER.next && mv $LEDGER.next $LEDGER"
    note "ledger now: $(box "cat $LEDGER" | tr '\n' ' ')"
}

held() { box "docker images --filter reference='$REPO:*' --format '{{.Repository}}:{{.Tag}}'"; }
