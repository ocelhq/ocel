#!/usr/bin/env bash
set -euo pipefail

IMAGE="${OCEL_ACT_IMAGE:-ghcr.io/catthehacker/ubuntu:act-latest}"
DOCKER_SOCK="${OCEL_ACT_DOCKER_SOCK:-/var/run/docker.sock}"
COMPOSE_PROJECT=ocel-act
DEV_LANE_PORTS=(5432 5433 3000 9000 9001 8000)
ALL_WORKFLOWS=(build go journey provider-aws provider-vps)

usage() {
    cat <<'EOF'
usage: scripts/act.sh [workflow ...]

Runs the PR gates locally before anything is pushed. build, go, journey and
provider-aws replay through nektos/act as workflow_dispatch, so every step runs
regardless of what changed — a superset of the PR run. provider-vps runs its
workflow's commands natively (incus wants systemd and KVM an act container
cannot host; the CI runner executes it un-containered too), and so does the
journey workflow's own vps lane, which is why journey replays its dev and aws
lanes only. The remote Go build cache is wired in from your local credentials,
so a green run both proves the change and leaves the cache warm for CI.

  workflows: build go journey provider-aws provider-vps    (default: all five)

journey and provider-aws drive the host docker daemon. journey needs the dev
compose stack's ports free — stop it first (docker compose stop); its own stack
runs under the ocel-act compose project and is torn down afterwards.
EOF
    exit 2
}

die() {
    echo "act.sh: $*" >&2
    exit 1
}

act_bin() {
    if command -v act >/dev/null 2>&1; then
        command -v act
    elif command -v mise >/dev/null 2>&1; then
        mise which act 2>/dev/null || true
    fi
}

cache_env() {
    local creds access_key secret_key session_token
    if creds=$(aws configure export-credentials --format env 2>/dev/null); then
        access_key=$(sed -n 's/^export AWS_ACCESS_KEY_ID=//p' <<<"$creds")
        secret_key=$(sed -n 's/^export AWS_SECRET_ACCESS_KEY=//p' <<<"$creds")
        session_token=$(sed -n 's/^export AWS_SESSION_TOKEN=//p' <<<"$creds")
    elif [ -n "${AWS_ACCESS_KEY_ID:-}" ] && [ -n "${AWS_SECRET_ACCESS_KEY:-}" ]; then
        access_key=$AWS_ACCESS_KEY_ID
        secret_key=$AWS_SECRET_ACCESS_KEY
        session_token=${AWS_SESSION_TOKEN:-}
    else
        access_key=$(aws configure get aws_access_key_id 2>/dev/null || true)
        secret_key=$(aws configure get aws_secret_access_key 2>/dev/null || true)
        session_token=$(aws configure get aws_session_token 2>/dev/null || true)
    fi
    [ -n "$access_key" ] && [ -n "$secret_key" ] ||
        die "no AWS credentials for the build cache — log in, or unset GOBUILDCACHE_S3_BUCKET to run uncached"
    printf '%s\n' \
        --env GOCACHEPROG=/opt/gobuildcache \
        --env GOBUILDCACHE_BACKEND_TYPE=s3 \
        --env "GOBUILDCACHE_S3_BUCKET=$GOBUILDCACHE_S3_BUCKET" \
        --env "GOBUILDCACHE_AWS_REGION=$GOBUILDCACHE_AWS_REGION" \
        --env "GOBUILDCACHE_AWS_ACCESS_KEY_ID=$access_key" \
        --env "GOBUILDCACHE_AWS_SECRET_ACCESS_KEY=$secret_key" \
        --env GOBUILDCACHE_PRINT_STATS=true
    if [ -n "$session_token" ]; then
        printf '%s\n' --env "GOBUILDCACHE_AWS_SESSION_TOKEN=$session_token"
    fi
}

incus_run() {
    if incus info >/dev/null 2>&1; then
        bash -c "$1"
    else
        sg incus-admin -c "$1"
    fi
}

run_provider_vps() {
    eval "$(mise env -s bash 2>/dev/null || true)"
    [ -e /dev/kvm ] || die "provider-vps needs /dev/kvm"
    incus_run "incus list" >/dev/null || die "provider-vps needs a working incus (incus admin init --auto)"
    local out status=0
    out=$(mktemp -d)
    pnpm install --frozen-lockfile &&
        pnpm --filter ocel build &&
        go generate -C cli ./... &&
        incus_run "scripts/incus.sh run ocel-act-live-$$ -- go test -C platform/vps/provider -race -count=1 -timeout 30m -run '^TestLive' -json ./..." | tee "$out/live.json" &&
        scripts/assert-ran.sh "$out/live.json" TestLive &&
        incus_run "scripts/incus.sh run ocel-act-lifecycle-$$ -- go test -C platform/vps/provider -race -count=1 -timeout 30m -run '^TestLifecycle' -json ./..." | tee "$out/lifecycle.json" &&
        scripts/assert-ran.sh "$out/lifecycle.json" TestLifecycle || status=$?
    rm -rf "$out"
    return $status
}

dev_lane_ports_free() {
    local port busy=()
    for port in "${DEV_LANE_PORTS[@]}"; do
        if ss -ltn "sport = :$port" 2>/dev/null | grep -q LISTEN; then
            busy+=("$port")
        fi
    done
    [ ${#busy[@]} -eq 0 ] ||
        die "the journey dev lane needs ports ${busy[*]} — stop whatever holds them (the dev stack: docker compose stop)"
}

case "${1:-}" in -h | --help) usage ;; esac

selected=("${@:-}")
[ -n "${selected[0]:-}" ] || selected=("${ALL_WORKFLOWS[@]}")
for wf in "${selected[@]}"; do
    case " ${ALL_WORKFLOWS[*]} " in
    *" $wf "*) [ -f ".github/workflows/$wf.yml" ] || die "no workflow file for $wf" ;;
    *) usage ;;
    esac
done

ACT=$(act_bin)
[ -n "$ACT" ] || die "act is not installed (mise install)"
[ -S "$DOCKER_SOCK" ] || die "no docker socket at $DOCKER_SOCK"

cd "$(dirname "$0")/.."

if [ -z "${GOBUILDCACHE_S3_BUCKET:-}" ] && command -v mise >/dev/null 2>&1; then
    eval "$(mise env -s bash 2>/dev/null || true)"
fi

CACHE_BIN=""
CACHE_ENV=()
if [ -n "${GOBUILDCACHE_S3_BUCKET:-}" ] && [ -n "${GOBUILDCACHE_AWS_REGION:-}" ]; then
    CACHE_BIN=$(command -v gobuildcache 2>/dev/null || mise which gobuildcache 2>/dev/null) ||
        die "gobuildcache is configured but the binary is missing (mise install)"
    cache_env_lines=$(cache_env)
    mapfile -t CACHE_ENV <<<"$cache_env_lines"
else
    echo "act.sh: no GOBUILDCACHE_S3_BUCKET/GOBUILDCACHE_AWS_REGION in the environment" >&2
    echo "act.sh: verifying only — the build cache will not be warmed (scripts/gobuildcache-setup.sh configures it)" >&2
fi

failed=()
for wf in "${selected[@]}"; do
    echo "act.sh: ▸ $wf"
    if [ "$wf" = provider-vps ]; then
        run_provider_vps || failed+=("$wf")
        continue
    fi
    container_opts="--init"
    wf_args=()
    case "$wf" in
    go | journey)
        if [ -n "$CACHE_BIN" ]; then
            container_opts="--init -v $CACHE_BIN:/opt/gobuildcache:ro"
            wf_args+=("${CACHE_ENV[@]}")
        fi
        ;;
    esac
    if [ "$wf" = journey ]; then
        dev_lane_ports_free
        wf_args+=(--env "COMPOSE_PROJECT_NAME=$COMPOSE_PROJECT" --env "OCEL_JOURNEY_LANES=dev aws")
    fi
    if ! "$ACT" workflow_dispatch \
        -W ".github/workflows/$wf.yml" \
        -P "ubuntu-latest=$IMAGE" \
        --container-daemon-socket "$DOCKER_SOCK" \
        --network host \
        --container-options "$container_opts" \
        "${wf_args[@]}"; then
        failed+=("$wf")
    fi
    if [ "$wf" = journey ]; then
        docker compose -p "$COMPOSE_PROJECT" down -v --remove-orphans >/dev/null 2>&1 || true
    fi
done

if [ ${#failed[@]} -gt 0 ]; then
    die "failed: ${failed[*]}"
fi
if [ -n "$CACHE_BIN" ]; then
    echo "act.sh: ✓ ${selected[*]} — green, cache warm"
else
    echo "act.sh: ✓ ${selected[*]} — green (uncached)"
fi
