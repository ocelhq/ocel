#!/usr/bin/env bash
set -euo pipefail

STATE_DIR="${OCEL_INCUS_STATE:-${XDG_STATE_HOME:-$HOME/.local/state}/ocel-incus}"
KEY="$STATE_DIR/id_ed25519"
IMAGE="images:ubuntu/24.04/cloud"
SSH_USER=ubuntu
SSH_WAIT_SECS="${OCEL_INCUS_SSH_WAIT:-300}"

usage() {
    cat <<'EOF'
usage: scripts/incus.sh <command> [args]

  fetch                  pull the VM image into the local store once
  create <name>          create VM, inject key via cloud-init, wait for SSH,
                         snapshot 'clean', print info lines
  restore <name>         restore the 'clean' snapshot, wait for SSH
  info <name>            print OCEL_INCUS_{NAME,ADDR,USER,KEY}= lines (eval-able)
  ssh <name> [cmd...]    SSH into the VM
  destroy <name>         delete the VM (incus delete -f), idempotent
  bake <name> -- cmd...  create, run cmd on the VM over SSH, then stop it
                         ready to be cloned
  clone <base> <name>    copy a baked VM, start it, wait for SSH, print info
  run [--from <base>] <name> -- cmd...
                         create (or clone <base>), run cmd with OCEL_INCUS_*
                         exported, destroy on exit no matter what
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
    incus list "^$1\$" -c4 -f csv | tr -d '"' |
        awk '!/\((lo|docker[0-9]|br-|veth)/ && !found { print $1; found = 1 }'
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

cloud_init_settled() {
    incus exec "$1" -- cloud-init status 2>/dev/null |
        grep -qE '^status: (done|error|degraded)'
}

wait_ssh() {
    local name=$1 addr grace=15 deadline=$((SECONDS + SSH_WAIT_SECS))
    while [ "$SECONDS" -lt "$deadline" ]; do
        addr=$(addr_of "$name")
        if [ -n "$addr" ]; then
            mapfile -t opts < <(ssh_opts)
            if ssh "${opts[@]}" -o ConnectTimeout=3 "$SSH_USER@$addr" true 2>/dev/null; then
                echo "$addr"
                return 0
            fi
        fi
        if [ "$deadline" -gt $((SECONDS + grace)) ] && cloud_init_settled "$name"; then
            deadline=$((SECONDS + grace))
        fi
        sleep 2
    done
    diagnose_no_ssh "$name" >&2
    die "$name: no SSH after ${SSH_WAIT_SECS}s"
}

diagnose_no_ssh() {
    local name=$1
    echo "incus.sh: cloud-init installs sshd over the network, so no SSH usually"
    echo "incus.sh: means the VM has no egress. cloud-init reports:"
    incus exec "$name" -- cloud-init status --long 2>&1 | sed 's/^/    /' || true
    echo "incus.sh: check egress with:"
    echo "incus.sh:   incus exec $name -- curl -4 -sS -o /dev/null -w '%{http_code}\\n' http://archive.ubuntu.com/ubuntu/"
}

print_info() {
    local name=$1 addr=$2
    printf 'OCEL_INCUS_NAME=%s\n' "$name"
    printf 'OCEL_INCUS_ADDR=%s\n' "$addr"
    printf 'OCEL_INCUS_USER=%s\n' "$SSH_USER"
    printf 'OCEL_INCUS_KEY=%s\n' "$KEY"
}

cmd_fetch() {
    incus image copy "$IMAGE" local: --vm
}

cmd_create() {
    local name=$1
    ensure_key
    trap 'discard_half_made "'"$name"'" $?' EXIT
    trap 'exit 130' INT
    trap 'exit 143' TERM
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
runcmd:
  - [ usermod, -p, '*', $SSH_USER ]
EOF
    incus start "$name"
    local addr
    addr=$(wait_ssh "$name")
    incus exec "$name" -- sync
    incus snapshot create "$name" clean
    trap - EXIT
    print_info "$name" "$addr"
}

discard_half_made() {
    local name=$1 status=$2
    trap - EXIT
    [ "$status" -eq 0 ] && return 0
    if [ -n "${OCEL_INCUS_KEEP:-}" ]; then
        echo "incus.sh: leaving $name behind to inspect (OCEL_INCUS_KEEP is set)" >&2
        return 0
    fi
    echo "incus.sh: deleting half-made $name (OCEL_INCUS_KEEP=1 keeps it)" >&2
    incus delete -f "$name" 2>/dev/null || true
    return 0
}

cmd_bake() {
    local name=$1
    shift
    [ "${1:-}" = "--" ] || usage
    shift
    [ $# -gt 0 ] || usage
    trap 'discard_half_made "'"$name"'" $?' EXIT
    trap 'exit 130' INT
    trap 'exit 143' TERM
    cmd_create "$name" > /dev/null
    trap 'discard_half_made "'"$name"'" $?' EXIT
    cmd_ssh "$name" "$@"
    cmd_ssh "$name" 'sudo cloud-init clean --logs --configs network && sudo truncate -s 0 /etc/machine-id && sudo rm -f /var/lib/dbus/machine-id'
    incus stop "$name"
    trap - EXIT
}

cmd_clone() {
    local base=$1 name=$2
    ensure_key
    trap 'discard_half_made "'"$name"'" $?' EXIT
    trap 'exit 130' INT
    trap 'exit 143' TERM
    incus copy "$base" "$name"
    incus start "$name"
    wait_ssh "$name" > /dev/null
    cloud_init_finished "$name"
    local addr
    addr=$(wait_ssh "$name")
    trap - EXIT
    print_info "$name" "$addr"
}

cloud_init_finished() {
    local name=$1 rc=0
    incus exec "$name" -- cloud-init status --wait > /dev/null || rc=$?
    [ "$rc" -eq 0 ] || [ "$rc" -eq 2 ] || die "$name: cloud-init ended with status $rc"
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
    local base=""
    if [ "${1:-}" = "--from" ]; then
        base=${2:-}
        [ -n "$base" ] || usage
        shift 2
    fi
    local name=$1
    shift
    [ "${1:-}" = "--" ] || usage
    shift
    [ $# -gt 0 ] || usage
    trap 'incus delete -f "'"$name"'" 2>/dev/null || true' EXIT
    trap 'exit 130' INT
    trap 'exit 143' TERM
    local addr
    if [ -n "$base" ]; then
        addr=$(cmd_clone "$base" "$name" | sed -n 's/^OCEL_INCUS_ADDR=//p')
    else
        addr=$(cmd_create "$name" | sed -n 's/^OCEL_INCUS_ADDR=//p')
    fi
    [ -n "$addr" ] || die "$name: created without an address, so there is nothing to hand the command"
    OCEL_INCUS_NAME=$name \
        OCEL_INCUS_ADDR=$addr \
        OCEL_INCUS_USER=$SSH_USER \
        OCEL_INCUS_KEY=$KEY \
        "$@"
}

[ $# -ge 1 ] || usage
cmd=$1
shift
case "$cmd" in
fetch) [ $# -eq 0 ] || usage; cmd_fetch ;;
create) [ $# -eq 1 ] || usage; cmd_create "$@" ;;
bake) cmd_bake "$@" ;;
clone) [ $# -eq 2 ] || usage; cmd_clone "$@" ;;
restore) cmd_restore "$@" ;;
info) cmd_info "$@" ;;
ssh) cmd_ssh "$@" ;;
destroy) cmd_destroy "$@" ;;
run) cmd_run "$@" ;;
*) usage ;;
esac
