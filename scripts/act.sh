#!/usr/bin/env bash
set -euo pipefail

IMAGE="${OCEL_ACT_IMAGE:-ghcr.io/catthehacker/ubuntu:act-latest}"
DOCKER_SOCK="${OCEL_ACT_DOCKER_SOCK:-/var/run/docker.sock}"
ALL_WORKFLOWS=(unit e2e integration)

usage() {
    cat <<'EOF'
usage: scripts/act.sh [workflow ...]

Runs the PR gates locally before anything is pushed. unit replays the Unit
Tests workflow through nektos/act as workflow_dispatch, so every job runs
regardless of what changed. e2e and integration run natively instead: e2e
drives the dev, aws (on floci) and vps (on an incus VM) targets with snapshot
binaries built here, since its jobs wait on an artifact act cannot serve, and
integration runs the vps live suite on an incus VM, since incus wants systemd
and KVM an act container cannot host. The remote Go build cache is wired into
the act replay from your local credentials, so a green run both proves the
change and leaves the cache warm for CI.

  workflows: unit e2e integration    (default: all three)

unit and e2e drive the host docker daemon; the e2e dev target runs its
postgres and bucket there, on ports docker picks.
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

run_integration() {
    eval "$(mise env -s bash 2>/dev/null || true)"
    [ -e /dev/kvm ] || die "integration needs /dev/kvm"
    incus_run "incus list" >/dev/null || die "integration needs a working incus (incus admin init --auto)"
    pnpm install --frozen-lockfile &&
        pnpm turbo run build --filter=ocel &&
        go generate -C cli ./... &&
        incus_run "scripts/incus.sh run ocel-act-live-$$ -- go test -C platform/vps/provider -race -count=1 -timeout 30m -tags integration -run '^TestLive' ./..."
}

run_e2e() {
    eval "$(mise env -s bash 2>/dev/null || true)"
    [ -e /dev/kvm ] || die "the e2e vps target needs /dev/kvm"
    incus_run "incus list" >/dev/null || die "the e2e vps target needs a working incus (incus admin init --auto)"
    pnpm install --frozen-lockfile &&
        pnpm turbo run build --filter=ocel --filter=@ocel/transforms --filter=@ocel/sst --filter=@ocel/pulumi || return $?
    local snapshot status=0
    snapshot=$(node scripts/snapshot.mjs) || return $?
    eval "$snapshot"
    export OCEL_BIN OCEL_PROVIDERS_DIR
    OCEL_TARGET=dev pnpm --filter @ocel-tests/e2e e2e || status=$?
    OCEL_TARGET=aws scripts/floci.sh run "ocel-act-e2e-$$" -- \
        bash -c 'AWS_ENDPOINT_URL=${OCEL_FLOCI_ENDPOINT/127.0.0.1/localhost.localstack.cloud} pnpm --filter @ocel-tests/e2e e2e' || status=$?
    local vm=ocel-act-e2e-vps-$$
    incus_run "scripts/incus.sh create $vm" || return $?
    eval "$(incus_run "scripts/incus.sh info $vm")"
    OCEL_TARGET=vps \
        OCEL_VPS_HOST="$OCEL_INCUS_ADDR" \
        OCEL_VPS_USER="$OCEL_INCUS_USER" \
        OCEL_VPS_IDENTITY_FILE="$OCEL_INCUS_KEY" \
        pnpm --filter @ocel-tests/e2e e2e || status=$?
    incus_run "scripts/incus.sh destroy $vm" || echo "act.sh: could not destroy $vm" >&2
    return $status
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
    case "$wf" in
    integration)
        run_integration || failed+=("$wf")
        continue
        ;;
    e2e)
        run_e2e || failed+=("$wf")
        continue
        ;;
    esac
    container_opts="--init"
    wf_args=()
    if [ -n "$CACHE_BIN" ]; then
        container_opts="--init -v $CACHE_BIN:/opt/gobuildcache:ro"
        wf_args+=("${CACHE_ENV[@]}")
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
done

if [ ${#failed[@]} -gt 0 ]; then
    die "failed: ${failed[*]}"
fi
if [ -n "$CACHE_BIN" ]; then
    echo "act.sh: ✓ ${selected[*]} — green, cache warm"
else
    echo "act.sh: ✓ ${selected[*]} — green (uncached)"
fi
