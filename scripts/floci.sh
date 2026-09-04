#!/usr/bin/env bash
set -euo pipefail

IMAGE="${OCEL_FLOCI_IMAGE:-floci/floci:latest}"
DOCKER_SOCK="${OCEL_FLOCI_DOCKER_SOCK:-/var/run/docker.sock}"
READY_WAIT_SECS="${OCEL_FLOCI_READY_WAIT:-180}"

usage() {
    cat <<'EOF'
usage: scripts/floci.sh <command> [args]

  create <name>          run a floci container on a port of its own, wait for
                         every service to answer, print info lines
  status <name>          print OCEL_FLOCI_{NAME,ENDPOINT}= lines (eval-able)
  destroy <name>         remove the container (docker rm -f), idempotent
  run <name> -- cmd...   create, run cmd with OCEL_FLOCI_ENDPOINT exported,
                         destroy on exit no matter what
EOF
    exit 2
}

die() {
    echo "floci.sh: $*" >&2
    exit 1
}

endpoint_of() {
    local mapped
    mapped=$(docker port "$1" 4566/tcp 2>/dev/null | head -n1) || return 1
    [ -n "$mapped" ] || return 1
    printf 'http://127.0.0.1:%s\n' "${mapped##*:}"
}

answering() {
    curl -sf --max-time 5 "$1/_localstack/health" |
        grep -qE '"(cloudformation|s3|dynamodb|ssm|iam)":'
}

wait_ready() {
    local name=$1 endpoint began=$SECONDS deadline=$((SECONDS + READY_WAIT_SECS))
    while [ "$SECONDS" -lt "$deadline" ]; do
        if [ "$(docker inspect -f '{{.State.Running}}' "$name" 2>/dev/null)" != true ]; then
            diagnose_dead "$name" >&2
            die "$name: the container stopped before it answered"
        fi
        if endpoint=$(endpoint_of "$name") && answering "$endpoint"; then
            echo "floci.sh: $name answered after $((SECONDS - began))s" >&2
            echo "$endpoint"
            return 0
        fi
        sleep 1
    done
    diagnose_dead "$name" >&2
    die "$name: nothing answered on 4566 after ${READY_WAIT_SECS}s"
}

diagnose_dead() {
    echo "floci.sh: the emulator's last words:"
    docker logs --tail 40 "$1" 2>&1 | sed 's/^/    /' || true
}

print_info() {
    printf 'OCEL_FLOCI_NAME=%s\n' "$1"
    printf 'OCEL_FLOCI_ENDPOINT=%s\n' "$2"
}

cmd_create() {
    local name=$1
    trap 'discard_half_made "'"$name"'" $?' EXIT
    trap 'exit 130' INT
    trap 'exit 143' TERM
    local mounts=()
    if [ -S "$DOCKER_SOCK" ]; then
        mounts=(-v "$DOCKER_SOCK:/var/run/docker.sock")
    fi
    docker run -d --name "$name" -p 127.0.0.1::4566 "${mounts[@]}" "$IMAGE" >/dev/null
    local endpoint
    endpoint=$(wait_ready "$name")
    trap - EXIT
    print_info "$name" "$endpoint"
}

discard_half_made() {
    local name=$1 status=$2
    trap - EXIT
    [ "$status" -eq 0 ] && return 0
    if [ -n "${OCEL_FLOCI_KEEP:-}" ]; then
        echo "floci.sh: leaving $name behind to inspect (OCEL_FLOCI_KEEP is set)" >&2
        return 0
    fi
    echo "floci.sh: removing half-made $name (OCEL_FLOCI_KEEP=1 keeps it)" >&2
    docker rm -f "$name" >/dev/null 2>&1 || true
    return 0
}

cmd_status() {
    local name=$1 endpoint
    endpoint=$(endpoint_of "$name") || die "$name: no published port (is it running?)"
    print_info "$name" "$endpoint"
}

cmd_destroy() {
    local name=$1
    if docker inspect "$name" >/dev/null 2>&1; then
        docker rm -f "$name" >/dev/null
    fi
}

cmd_run() {
    local name=$1
    shift
    [ "${1:-}" = "--" ] || usage
    shift
    [ $# -gt 0 ] || usage
    trap 'docker rm -f "'"$name"'" >/dev/null 2>&1 || true' EXIT
    trap 'exit 130' INT
    trap 'exit 143' TERM
    local endpoint
    endpoint=$(cmd_create "$name" | sed -n 's/^OCEL_FLOCI_ENDPOINT=//p')
    [ -n "$endpoint" ] || die "$name: created without an endpoint, so there is nothing to hand the command"
    OCEL_FLOCI_ENDPOINT=$endpoint "$@"
}

[ $# -ge 2 ] || usage
cmd=$1
shift
case "$cmd" in
create) cmd_create "$@" ;;
status) cmd_status "$@" ;;
destroy) cmd_destroy "$@" ;;
run) cmd_run "$@" ;;
*) usage ;;
esac
