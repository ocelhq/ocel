#!/usr/bin/env bash
set -euo pipefail

STATE_DIR="${OCEL_INCUS_STATE:-${XDG_STATE_HOME:-$HOME/.local/state}/ocel-incus}"
KEY="$STATE_DIR/id_ed25519"
IMAGE="images:ubuntu/24.04/cloud"
SSH_USER=ubuntu
SSH_WAIT_SECS=300

usage() {
    cat <<'EOF'
usage: scripts/incus.sh <command> [args]

  create <name>          create VM, inject key via cloud-init, wait for SSH,
                         snapshot 'clean', print info lines
  restore <name>         restore the 'clean' snapshot, wait for SSH
  info <name>            print OCEL_INCUS_{NAME,ADDR,USER,KEY}= lines (eval-able)
  ssh <name> [cmd...]    SSH into the VM
  destroy <name>         delete the VM (incus delete -f), idempotent
  run <name> -- cmd...   create, run cmd with OCEL_INCUS_* exported,
                         destroy on exit no matter what
EOF
    exit 2
}

die() {
    echo "incus.sh: $*" >&2
    exit 1
}

ensure_key() {
    mkdir -p "$STATE_DIR"
    [ -f "$KEY" ] || ssh-keygen -q -t ed25519 -f "$KEY" -N '' -C ocel-incus
}

addr_of() {
    incus list "^$1\$" -c4 -f csv | tr -d '"' | head -n1 | cut -d' ' -f1
}

ssh_opts() {
    printf '%s\n' \
        -i "$KEY" \
        -o IdentitiesOnly=yes \
        -o BatchMode=yes \
        -o StrictHostKeyChecking=no \
        -o UserKnownHostsFile=/dev/null \
        -o LogLevel=ERROR
}

wait_ssh() {
    local name=$1 addr deadline=$((SECONDS + SSH_WAIT_SECS))
    while [ "$SECONDS" -lt "$deadline" ]; do
        addr=$(addr_of "$name")
        if [ -n "$addr" ]; then
            mapfile -t opts < <(ssh_opts)
            if ssh "${opts[@]}" -o ConnectTimeout=3 "$SSH_USER@$addr" true 2>/dev/null; then
                echo "$addr"
                return 0
            fi
        fi
        sleep 2
    done
    die "$name: no SSH after ${SSH_WAIT_SECS}s"
}

print_info() {
    local name=$1 addr=$2
    printf 'OCEL_INCUS_NAME=%s\n' "$name"
    printf 'OCEL_INCUS_ADDR=%s\n' "$addr"
    printf 'OCEL_INCUS_USER=%s\n' "$SSH_USER"
    printf 'OCEL_INCUS_KEY=%s\n' "$KEY"
}

cmd_create() {
    local name=$1
    ensure_key
    incus init "$IMAGE" "$name" --vm \
        -c limits.cpu=2 \
        -c limits.memory=2GiB \
        -d root,size=20GiB
    incus config set "$name" cloud-init.user-data - <<EOF
#cloud-config
ssh_authorized_keys:
  - $(cat "$KEY.pub")
ssh_pwauth: false
packages:
  - openssh-server
EOF
    incus start "$name"
    local addr
    addr=$(wait_ssh "$name")
    incus snapshot create "$name" clean
    print_info "$name" "$addr"
}

cmd_restore() {
    local name=$1
    incus stop -f "$name" 2>/dev/null || true
    incus snapshot restore "$name" clean
    [ "$(incus list "^$name\$" -cs -f csv)" = RUNNING ] || incus start "$name"
    local addr
    addr=$(wait_ssh "$name")
    print_info "$name" "$addr"
}

cmd_info() {
    local name=$1 addr
    addr=$(addr_of "$name")
    [ -n "$addr" ] || die "$name: no address (is it running?)"
    print_info "$name" "$addr"
}

cmd_ssh() {
    local name=$1 addr
    shift
    addr=$(addr_of "$name")
    [ -n "$addr" ] || die "$name: no address (is it running?)"
    mapfile -t opts < <(ssh_opts)
    ssh "${opts[@]}" "$SSH_USER@$addr" "$@"
}

cmd_destroy() {
    local name=$1
    if incus info "$name" >/dev/null 2>&1; then
        incus delete -f "$name"
    fi
}

cmd_run() {
    local name=$1
    shift
    [ "${1:-}" = "--" ] || usage
    shift
    [ $# -gt 0 ] || usage
    trap 'incus delete -f "'"$name"'" 2>/dev/null || true' EXIT
    local addr
    addr=$(cmd_create "$name" | sed -n 's/^OCEL_INCUS_ADDR=//p')
    OCEL_INCUS_NAME=$name \
        OCEL_INCUS_ADDR=$addr \
        OCEL_INCUS_USER=$SSH_USER \
        OCEL_INCUS_KEY=$KEY \
        "$@"
}

[ $# -ge 2 ] || usage
cmd=$1
shift
case "$cmd" in
create) cmd_create "$@" ;;
restore) cmd_restore "$@" ;;
info) cmd_info "$@" ;;
ssh) cmd_ssh "$@" ;;
destroy) cmd_destroy "$@" ;;
run) cmd_run "$@" ;;
*) usage ;;
esac
