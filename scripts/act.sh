#!/usr/bin/env bash
set -euo pipefail

IMAGE="${OCEL_ACT_IMAGE:-ghcr.io/catthehacker/ubuntu:act-latest}"
DOCKER_SOCK="${OCEL_ACT_DOCKER_SOCK:-/var/run/docker.sock}"
ALL_WORKFLOWS=(build go e2e aws-live)

usage() {
    cat <<'EOF'
usage: scripts/act.sh [workflow ...]

Replays the PR gates locally through nektos/act before anything is pushed.
Workflows run as workflow_dispatch, so every step runs regardless of what
changed — a superset of the PR run. The remote Go build cache is wired in
from your local credentials, so a green run both proves the change and
leaves the cache warm for CI.

  workflows: build go e2e aws-live    (default: all four)

vps-live is not replayable — incus wants systemd and KVM the act container
cannot host. Exercise it natively, the way the workflow does:

    scripts/incus.sh run <name> -- go test -C platform/vps/provider \
      -race -count=1 -run '^TestLive' ./...

e2e and aws-live drive the host docker daemon on its fixed ports
(5432/5433/3000/9000, and floci's mapped ports) — stop the dev compose
stack before running them.
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

cache_args() {
    if [ -z "${GOBUILDCACHE_S3_BUCKET:-}" ] && command -v mise >/dev/null 2>&1; then
        eval "$(mise env -s bash 2>/dev/null || true)"
    fi
    if [ -z "${GOBUILDCACHE_S3_BUCKET:-}" ] || [ -z "${GOBUILDCACHE_AWS_REGION:-}" ]; then
        echo "act.sh: no GOBUILDCACHE_S3_BUCKET/GOBUILDCACHE_AWS_REGION in the environment" >&2
        echo "act.sh: verifying only — the build cache will not be warmed (scripts/gobuildcache-setup.sh configures it)" >&2
        return 0
    fi
    local bin
    bin=$(command -v gobuildcache 2>/dev/null || mise which gobuildcache 2>/dev/null) ||
        die "gobuildcache is configured but the binary is missing (mise install)"
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
        --container-options "-v $bin:/opt/gobuildcache:ro" \
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

mapfile -t extra < <(cache_args)

failed=()
for wf in "${selected[@]}"; do
    echo "act.sh: ▸ $wf"
    if ! "$ACT" workflow_dispatch \
        -W ".github/workflows/$wf.yml" \
        -P "ubuntu-latest=$IMAGE" \
        --container-daemon-socket "$DOCKER_SOCK" \
        --network host \
        "${extra[@]}"; then
        failed+=("$wf")
    fi
done

if [ ${#failed[@]} -gt 0 ]; then
    die "failed: ${failed[*]}"
fi
echo "act.sh: ✓ ${selected[*]} — green, cache warm"
