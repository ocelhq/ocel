#!/usr/bin/env bash
set -euo pipefail

AWS_IMAGE="${OCEL_FLOCI_IMAGE:-ghcr.io/ocelhq/floci:2.1.0-ocel.1}"
GCP_IMAGE="${OCEL_FLOCI_GCP_IMAGE:-floci/floci-gcp:0.8.0}"
FIRESTORE_IMAGE="${OCEL_FLOCI_FIRESTORE_IMAGE:-gcr.io/google.com/cloudsdktool/google-cloud-cli:587.0.0-emulators}"
DOCKER_SOCK="${OCEL_FLOCI_DOCKER_SOCK:-/var/run/docker.sock}"
READY_WAIT_SECS="${OCEL_FLOCI_READY_WAIT:-180}"

GCP_PROJECT=floci-local

usage() {
    cat <<'EOF'
usage: scripts/floci.sh [--cloud aws|gcp] <command> [args]

  create <name>          run a floci container on a port of its own, wait for
                         every service to answer, print info lines
  status <name>          print OCEL_FLOCI_{NAME,ENDPOINT}= lines (eval-able)
  destroy <name>         remove the container (docker rm -f), idempotent
  run <name> -- cmd...   create, run cmd with the endpoint exported, destroy on
                         exit no matter what

The aws emulator answers on 4566 and exports OCEL_FLOCI_ENDPOINT; the gcp one
answers on 4588 and exports OCEL_FLOCI_GCP_ENDPOINT. They are separate images
and a run of one is invisible to the other. The aws emulator also publishes
6379-6399 on the host, where its ElastiCache caches answer, so only one aws
emulator runs on a host at a time. The gcp emulator also runs Google's Firestore
emulator in <name>-firestore, which keeps the Next tag records floci-gcp cannot
serve over REST, and exports OCEL_FLOCI_FIRESTORE_ENDPOINT.
EOF
    exit 2
}

die() {
    echo "floci.sh: $*" >&2
    exit 1
}

CLOUD=aws
if [ "${1:-}" = "--cloud" ]; then
    CLOUD=${2:-}
    shift 2 || usage
fi

case "$CLOUD" in
aws)
    IMAGE=$AWS_IMAGE
    PORT=4566
    ENDPOINT_VAR=OCEL_FLOCI_ENDPOINT
    MOUNTS_DOCKER=yes
    READY_PATH=/_localstack/health
    READY_BODIES=()
    for service in cloudformation s3 dynamodb ssm iam lambda sqs sns scheduler; do
        READY_BODIES+=("\"$service\": *\"running\"")
    done
    EXTRA_ARGS=(-p "127.0.0.1:6379-6399:6379-6399" -e FLOCI_HOSTNAME=localhost.localstack.cloud)
    FIRESTORE_PORT=
    ;;
gcp)
    IMAGE=$GCP_IMAGE
    PORT=4588
    FIRESTORE_PORT=8080
    FIRESTORE_VAR=OCEL_FLOCI_FIRESTORE_ENDPOINT
    ENDPOINT_VAR=OCEL_FLOCI_GCP_ENDPOINT
    MOUNTS_DOCKER=yes
    READY_PATH="/storage/v1/b?project=$GCP_PROJECT"
    READY_BODIES=('"kind": *"storage#buckets"')
    EXTRA_ARGS=()
    ;;
*) die "unknown cloud: $CLOUD (expected aws or gcp)" ;;
esac

endpoint_of() {
    local mapped
    mapped=$(docker port "$1" "$2/tcp" 2>/dev/null) || return 1
    mapped=${mapped%%$'\n'*}
    [ -n "$mapped" ] || return 1
    printf 'http://127.0.0.1:%s\n' "${mapped##*:}"
}

sidecar_of() { printf '%s-firestore\n' "$1"; }

answering() {
    local body
    body=$(curl -sf --max-time 5 "$1$READY_PATH") || return 1
    local pattern
    for pattern in "${READY_BODIES[@]}"; do
        grep -qE "$pattern" <<<"$body" || return 1
    done
}

wait_ready() {
    local name=$1 endpoint began=$SECONDS deadline=$((SECONDS + READY_WAIT_SECS))
    while [ "$SECONDS" -lt "$deadline" ]; do
        if [ "$(docker inspect -f '{{.State.Running}}' "$name" 2>/dev/null)" != true ]; then
            diagnose_dead "$name" >&2
            die "$name: the container stopped before it answered"
        fi
        if endpoint=$(endpoint_of "$name" "$PORT") && answering "$endpoint"; then
            echo "floci.sh: $name answered after $((SECONDS - began))s" >&2
            echo "$endpoint"
            return 0
        fi
        sleep 1
    done
    diagnose_dead "$name" >&2
    die "$name: nothing answered on $PORT after ${READY_WAIT_SECS}s"
}

wait_firestore_ready() {
    local name=$1 endpoint began=$SECONDS deadline=$((SECONDS + READY_WAIT_SECS))
    while [ "$SECONDS" -lt "$deadline" ]; do
        if [ "$(docker inspect -f '{{.State.Running}}' "$name" 2>/dev/null)" != true ]; then
            diagnose_dead "$name" >&2
            die "$name: the container stopped before it answered"
        fi
        if endpoint=$(endpoint_of "$name" "$FIRESTORE_PORT") && [ "$(curl -sf --max-time 5 "$endpoint/")" = Ok ]; then
            echo "floci.sh: $name answered after $((SECONDS - began))s" >&2
            echo "$endpoint"
            return 0
        fi
        sleep 1
    done
    diagnose_dead "$name" >&2
    die "$name: nothing answered on $FIRESTORE_PORT after ${READY_WAIT_SECS}s"
}

diagnose_dead() {
    echo "floci.sh: the emulator's last words:"
    docker logs --tail 40 "$1" 2>&1 | sed 's/^/    /' || true
}

print_info() {
    printf 'OCEL_FLOCI_NAME=%s\n' "$1"
    printf '%s=%s\n' "$ENDPOINT_VAR" "$2"
    if [ -n "${3:-}" ]; then
        printf '%s=%s\n' "$FIRESTORE_VAR" "$3"
    fi
}

published_to_containers() {
    local gateway port container=$1
    gateway=$(docker network inspect bridge -f '{{(index .IPAM.Config 0).Gateway}}' 2>/dev/null || true)
    if [ -z "$gateway" ]; then
        echo "-p 127.0.0.1::$container"
        return
    fi
    port=$(python3 -c 'import socket; s = socket.socket(); s.bind(("127.0.0.1", 0)); print(s.getsockname()[1])')
    echo "-p 127.0.0.1:$port:$container -p $gateway:$port:$container"
}

cmd_create() {
    local name=$1
    trap 'discard_half_made "'"$name"'" $?' EXIT
    trap 'exit 130' INT
    trap 'exit 143' TERM
    local mounts=()
    if [ "$MOUNTS_DOCKER" = yes ] && [ -S "$DOCKER_SOCK" ]; then
        mounts=(-v "$DOCKER_SOCK:/var/run/docker.sock")
    fi
    local published=(-p "127.0.0.1::$PORT")
    if [ "$CLOUD" = gcp ]; then
        read -ra published <<<"$(published_to_containers "$PORT")"
        local sidecar_published
        read -ra sidecar_published <<<"$(published_to_containers "$FIRESTORE_PORT")"
        docker run -d --name "$(sidecar_of "$name")" "${sidecar_published[@]}" "$FIRESTORE_IMAGE" \
            gcloud emulators firestore start --host-port=0.0.0.0:8080 >/dev/null
    fi
    docker run -d --name "$name" "${published[@]}" "${EXTRA_ARGS[@]}" "${mounts[@]}" "$IMAGE" >/dev/null
    local endpoint firestore_endpoint=
    endpoint=$(wait_ready "$name")
    if [ -n "$FIRESTORE_PORT" ]; then
        firestore_endpoint=$(wait_firestore_ready "$(sidecar_of "$name")")
    fi
    trap - EXIT
    print_info "$name" "$endpoint" "$firestore_endpoint"
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
    docker rm -f "$name" "$(sidecar_of "$name")" >/dev/null 2>&1 || true
    return 0
}

cmd_status() {
    local name=$1 endpoint firestore_endpoint=
    endpoint=$(endpoint_of "$name" "$PORT") || die "$name: no published port (is it running?)"
    if [ -n "$FIRESTORE_PORT" ]; then
        firestore_endpoint=$(endpoint_of "$(sidecar_of "$name")" "$FIRESTORE_PORT") ||
            die "$(sidecar_of "$name"): no published port (is it running?)"
    fi
    print_info "$name" "$endpoint" "$firestore_endpoint"
}

cmd_destroy() {
    local name=$1
    local container
    for container in "$name" "$(sidecar_of "$name")"; do
        if docker inspect "$container" >/dev/null 2>&1; then
            docker rm -f "$container" >/dev/null
        fi
    done
}

cmd_run() {
    local name=$1
    shift
    [ "${1:-}" = "--" ] || usage
    shift
    [ $# -gt 0 ] || usage
    trap 'cmd_destroy "'"$name"'" >/dev/null 2>&1 || true' EXIT
    trap 'exit 130' INT
    trap 'exit 143' TERM
    local info
    info=$(cmd_create "$name")
    grep -q "^$ENDPOINT_VAR=." <<<"$info" || die "$name: created without an endpoint, so there is nothing to hand the command"
    if [ -n "$FIRESTORE_PORT" ]; then
        grep -q "^$FIRESTORE_VAR=." <<<"$info" || die "$name: created without a Firestore endpoint, so there is nothing to hand the command"
    fi
    env $(grep -v '^OCEL_FLOCI_NAME=' <<<"$info") "$@"
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
