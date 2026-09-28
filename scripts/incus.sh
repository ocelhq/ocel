#!/usr/bin/env bash
set -euo pipefail

STATE_DIR="${OCEL_INCUS_STATE:-${XDG_STATE_HOME:-$HOME/.local/state}/ocel-incus}"
KEY="$STATE_DIR/id_ed25519"
IMAGE="images:ubuntu/24.04/cloud"
SSH_USER=ubuntu
SSH_WAIT_SECS="${OCEL_INCUS_SSH_WAIT:-300}"
STOCK_MIRROR="http://archive.ubuntu.com/ubuntu/"
APT_MIRROR="${OCEL_INCUS_APT_MIRROR:-}"

usage() {
    cat <<'EOF'
usage: scripts/incus.sh <command> [args]

  fetch                  pull the VM image into the local store once
  create [--from <image>] <name>
                         create VM (from a baked image), inject key via
                         cloud-init, wait for SSH, snapshot 'clean', print
                         info lines
  restore <name>         restore the 'clean' snapshot, wait for SSH
  info <name>            print OCEL_INCUS_{NAME,ADDR,USER,KEY}= lines (eval-able)
  ssh <name> [cmd...]    SSH into the VM
  destroy <name>         delete the VM (incus delete -f), idempotent
  bake <image> -- cmd... create a VM, run cmd on it over SSH, publish it as
                         the local image <image> and delete the VM
  save <image> <dir>     export the local image <image> into <dir>
  load <dir> <image>     import the image saved into <dir> as <image>
  run [--from <image>] <name> -- cmd...
                         create (from a baked image), run cmd with
                         OCEL_INCUS_* exported, destroy on exit no matter what
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
        awk '!/\((lo|docker|br-|veth)/ && !found { print $1; found = 1 }'
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

cloud_init_ended() {
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
        if [ "$deadline" -gt $((SECONDS + grace)) ] && cloud_init_ended "$name"; then
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
    diagnose_section "guest addresses and routes" \
        incus exec "$name" -- sh -c 'ip -4 -br addr; ip -4 route; cat /etc/resolv.conf'
    diagnose_section "guest name resolution" \
        incus exec "$name" -- getent hosts archive.ubuntu.com
    diagnose_section "guest egress to ${APT_MIRROR:-$STOCK_MIRROR} (status, connect, total, bytes/s)" \
        incus exec "$name" -- curl -4 -sS -m 30 -o /dev/null -w '%{http_code} %{time_connect} %{time_total} %{speed_download}\n' "${APT_MIRROR:-$STOCK_MIRROR}dists/noble/Release"
    diagnose_section "host egress to $STOCK_MIRROR (status, connect, total, bytes/s)" \
        curl -4 -sS -m 30 -o /dev/null -w '%{http_code} %{time_connect} %{time_total} %{speed_download}\n' "${STOCK_MIRROR}dists/noble/Release"
    diagnose_section "guest cloud-init log tail" \
        incus exec "$name" -- tail -n 30 /var/log/cloud-init.log
    diagnose_section "host bridge" \
        sh -c "incus network show incusbr0; ip -4 -br addr show incusbr0"
    diagnose_section "host forwarding" \
        sudo -n sh -c 'sysctl net.ipv4.ip_forward; nft list ruleset; iptables-save'
}

apt_mirror_config() {
    [ -n "$APT_MIRROR" ] || return 0
    printf 'apt:\n  primary:\n    - arches: [default]\n      uri: %s\n' "$APT_MIRROR"
}

diagnose_section() {
    local title=$1
    shift
    echo "incus.sh: --- $title"
    "$@" 2>&1 | sed 's/^/    /' || true
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

sshd_packages() {
    [ "$1" = "$IMAGE" ] || return 0
    printf 'packages:\n  - openssh-server\n'
}

cmd_create() {
    local image=$IMAGE
    if [ "${1:-}" = "--from" ]; then
        image=${2:-}
        [ -n "$image" ] || usage
        shift 2
    fi
    [ $# -eq 1 ] || usage
    local name=$1
    ensure_key
    trap 'discard_half_made "'"$name"'" $?' EXIT
    trap 'exit 130' INT
    trap 'exit 143' TERM
    incus init "$image" "$name" --vm \
        -c limits.cpu=2 \
        -c limits.memory=2GiB \
        -d root,size=20GiB
    incus config set "$name" cloud-init.user-data - <<EOF
#cloud-config
ssh_authorized_keys:
  - $(cat "$KEY.pub")
ssh_pwauth: false
$(apt_mirror_config)
$(sshd_packages "$image")
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
    local image=$1 name=$1-bake
    shift
    [ "${1:-}" = "--" ] || usage
    shift
    [ $# -gt 0 ] || usage
    trap 'incus delete -f "'"$name"'" 2>/dev/null || true' EXIT
    trap 'exit 130' INT
    trap 'exit 143' TERM
    cmd_create "$name" > /dev/null
    trap 'incus delete -f "'"$name"'" 2>/dev/null || true' EXIT
    cmd_ssh "$name" "$@"
    cmd_ssh "$name" 'sudo cloud-init clean --logs --configs network && sudo truncate -s 0 /etc/machine-id && sudo rm -f /var/lib/dbus/machine-id && rm -f ~/.ssh/authorized_keys'
    incus stop "$name"
    incus publish "$name" --alias "$image" --compression none
}

cmd_save() {
    local image=$1 dir=$2
    mkdir -p "$dir"
    incus image export "$image" "$dir/image"
}

cmd_load() {
    local dir=$1 image=$2
    local meta root="$dir/image.root"
    meta=$(find "$dir" -maxdepth 1 -name 'image*' ! -name image.root -print -quit)
    [ -n "$meta" ] && [ -e "$root" ] || die "$dir holds no saved VM image"
    incus image import "$meta" "$root" --alias "$image"
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
    local from=()
    if [ "${1:-}" = "--from" ]; then
        [ -n "${2:-}" ] || usage
        from=(--from "$2")
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
    addr=$(cmd_create "${from[@]}" "$name" | sed -n 's/^OCEL_INCUS_ADDR=//p')
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
create) cmd_create "$@" ;;
bake) [ $# -ge 1 ] || usage; cmd_bake "$@" ;;
save) [ $# -eq 2 ] || usage; cmd_save "$@" ;;
load) [ $# -eq 2 ] || usage; cmd_load "$@" ;;
restore) cmd_restore "$@" ;;
info) cmd_info "$@" ;;
ssh) cmd_ssh "$@" ;;
destroy) cmd_destroy "$@" ;;
run) cmd_run "$@" ;;
*) usage ;;
esac
