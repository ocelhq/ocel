#!/usr/bin/env bash
set -euo pipefail

STATE_ROOT="${OCEL_GCE_STATE:-${XDG_STATE_HOME:-$HOME/.local/state}/ocel-gce}"
ZONE="${OCEL_GCE_ZONE:-europe-west1-b}"
MACHINE_TYPE="${OCEL_GCE_MACHINE_TYPE:-e2-medium}"
IMAGE_FAMILY=ubuntu-2404-lts-amd64
IMAGE_PROJECT=ubuntu-os-cloud
NETWORK=default
SSH_USER=ubuntu
SSH_WAIT_SECS="${OCEL_GCE_SSH_WAIT:-300}"
ROOT_SIZE_GB=20
SWEEP_HOURS_DEFAULT=3

usage() {
    cat <<'EOF'
usage: scripts/gce.sh <command> [args]

  create <name>          launch an Ubuntu instance with a public IP, wait for
                         SSH, print info lines
  info <name>            print OCEL_GCE_{NAME,ADDR,USER,KEY,ZONE}= lines
                         (eval-able)
  ssh <name> [cmd...]    SSH into the instance
  destroy <name>         delete the instance and its firewall rule and state,
                         idempotent
  sweep [hours]          destroy every journey instance older than hours
                         (default 3), then the firewall rules nothing uses

This spends the Google Cloud project gcloud is configured with.
EOF
    exit 2
}

die() {
    echo "gce.sh: $*" >&2
    exit 1
}

box_dir() {
    printf '%s/%s\n' "$STATE_ROOT" "$1"
}

key_path() {
    printf '%s/id_ed25519\n' "$(box_dir "$1")"
}

resource_name() {
    printf 'ocel-gce-%s\n' "$1"
}

valid_name() {
    [[ $1 =~ ^[a-z0-9]([-a-z0-9]{0,52}[a-z0-9])?$ ]] ||
        die "$1: a name is lowercase letters, digits and dashes, at most 54 of them"
}

ensure_key() {
    local key
    key=$(key_path "$1")
    mkdir -p "$(box_dir "$1")"
    [ -f "$key" ] || ssh-keygen -q -t ed25519 -f "$key" -N '' -C "$(resource_name "$1")"
}

ssh_opts() {
    printf '%s\n' \
        -i "$1" \
        -o IdentitiesOnly=yes \
        -o BatchMode=yes \
        -o StrictHostKeyChecking=no \
        -o UserKnownHostsFile=/dev/null \
        -o LogLevel=ERROR
}

zone_of() {
    local file zone
    file="$(box_dir "$1")/zone"
    if [ -f "$file" ]; then
        cat "$file"
        return 0
    fi
    zone=$(gcloud compute instances list \
        --filter="name=$(resource_name "$1")" \
        --format='value(zone.basename())' | head -n1)
    printf '%s\n' "${zone:-$ZONE}"
}

addr_of() {
    gcloud compute instances describe "$(resource_name "$1")" \
        --zone "$(zone_of "$1")" \
        --format='value(networkInterfaces[0].accessConfigs[0].natIP)' 2>/dev/null
}

wait_ssh() {
    local name=$1 addr=$2 deadline=$((SECONDS + SSH_WAIT_SECS)) opts
    mapfile -t opts < <(ssh_opts "$(key_path "$name")")
    while [ "$SECONDS" -lt "$deadline" ]; do
        if ssh "${opts[@]}" -o ConnectTimeout=5 "$SSH_USER@$addr" 'cloud-init status --wait >/dev/null 2>&1; test $? -ne 1' 2>/dev/null; then
            return 0
        fi
        sleep 5
    done
    diagnose_no_ssh "$name" "$addr" >&2
    die "$name: no SSH after ${SSH_WAIT_SECS}s"
}

diagnose_no_ssh() {
    local name=$1 addr=$2
    echo "gce.sh: $addr answered no SSH, so the instance is still booting, the"
    echo "gce.sh: firewall does not let this network in, or cloud-init failed."
    echo "gce.sh: read the boot log with:"
    echo "gce.sh:   gcloud compute instances get-serial-port-output $(resource_name "$name") --zone $(zone_of "$name")"
}

print_info() {
    local name=$1 addr=$2
    printf 'OCEL_GCE_NAME=%s\n' "$name"
    printf 'OCEL_GCE_ADDR=%s\n' "$addr"
    printf 'OCEL_GCE_USER=%s\n' "$SSH_USER"
    printf 'OCEL_GCE_KEY=%s\n' "$(key_path "$name")"
    printf 'OCEL_GCE_ZONE=%s\n' "$(zone_of "$name")"
}

ensure_firewall() {
    local rule
    rule=$(resource_name "$1")
    gcloud compute firewall-rules describe "$rule" --format='value(name)' >/dev/null 2>&1 && return 0
    gcloud compute firewall-rules create "$rule" \
        --network "$NETWORK" \
        --direction INGRESS \
        --allow tcp:22,tcp:80,tcp:443 \
        --source-ranges 0.0.0.0/0 \
        --target-tags "$rule" \
        --description "ocel journey box $1" >/dev/null
}

cmd_create() {
    local name=$1 dir addr
    valid_name "$name"
    gcloud compute networks describe "$NETWORK" --format='value(name)' >/dev/null ||
        die "$name: no $NETWORK network in the project"
    ensure_key "$name"
    trap 'discard_half_made "'"$name"'" $?' EXIT
    trap 'exit 130' INT
    trap 'exit 143' TERM
    dir=$(box_dir "$name")
    printf '%s\n' "$ZONE" >"$dir/zone"
    printf '%s:%s\n' "$SSH_USER" "$(cat "$(key_path "$name").pub")" >"$dir/ssh-keys"
    gcloud compute instances create "$(resource_name "$name")" \
        --zone "$ZONE" \
        --machine-type "$MACHINE_TYPE" \
        --image-family "$IMAGE_FAMILY" \
        --image-project "$IMAGE_PROJECT" \
        --boot-disk-size "${ROOT_SIZE_GB}GB" \
        --boot-disk-type pd-balanced \
        --network "$NETWORK" \
        --tags "$(resource_name "$name")" \
        --labels "ocel-gce=journey,ocel-name=$name" \
        --no-service-account \
        --no-scopes \
        --metadata enable-oslogin=FALSE,block-project-ssh-keys=TRUE \
        --metadata-from-file "ssh-keys=$dir/ssh-keys" >/dev/null
    ensure_firewall "$name"
    addr=$(addr_of "$name")
    [ -n "$addr" ] || die "$name: running without a public IP"
    wait_ssh "$name" "$addr"
    trap - EXIT
    print_info "$name" "$addr"
}

discard_half_made() {
    local name=$1 status=$2
    trap - EXIT
    [ "$status" -eq 0 ] && return 0
    if [ -n "${OCEL_GCE_KEEP:-}" ]; then
        echo "gce.sh: leaving $name behind to inspect (OCEL_GCE_KEEP is set)" >&2
        return 0
    fi
    echo "gce.sh: destroying half-made $name (OCEL_GCE_KEEP=1 keeps it)" >&2
    cmd_destroy "$name" >&2 || true
    return 0
}

cmd_info() {
    local name=$1 addr
    addr=$(addr_of "$name")
    [ -n "$addr" ] || die "$name: no instance with a public IP (was it created, is it running?)"
    print_info "$name" "$addr"
}

cmd_ssh() {
    local name=$1 addr opts
    shift
    addr=$(addr_of "$name")
    [ -n "$addr" ] || die "$name: no instance with a public IP (was it created, is it running?)"
    mapfile -t opts < <(ssh_opts "$(key_path "$name")")
    ssh "${opts[@]}" "$SSH_USER@$addr" "$@"
}

cmd_destroy() {
    local name=$1 zone
    zone=$(zone_of "$name")
    if gcloud compute instances describe "$(resource_name "$name")" --zone "$zone" --format='value(name)' >/dev/null 2>&1; then
        gcloud compute instances delete "$(resource_name "$name")" --zone "$zone" --quiet
    fi
    if gcloud compute firewall-rules describe "$(resource_name "$name")" --format='value(name)' >/dev/null 2>&1; then
        gcloud compute firewall-rules delete "$(resource_name "$name")" --quiet
    fi
    rm -rf "$(box_dir "$name")"
}

sweep_instances() {
    local cutoff=$1 name created
    while IFS=$'\t' read -r name created; do
        [ -n "$name" ] || continue
        [ -n "$created" ] || continue
        [ "$(date -u -d "$created" +%s)" -lt "$cutoff" ] || continue
        cmd_destroy "$name"
        echo "gce.sh: reclaimed instance $name (created $created)"
    done < <(gcloud compute instances list \
        --filter='labels.ocel-gce=journey' \
        --format='value(labels.ocel-name,creationTimestamp)' | sort -u)
}

sweep_orphans() {
    local live rule
    live=$(gcloud compute instances list --filter='name~^ocel-gce-' --format='value(name)')
    while read -r rule; do
        [ -n "$rule" ] || continue
        grep -qxF "$rule" <<<"$live" && continue
        gcloud compute firewall-rules delete "$rule" --quiet
        echo "gce.sh: reclaimed firewall rule $rule"
    done < <(gcloud compute firewall-rules list --filter='name~^ocel-gce-' --format='value(name)')
}

cmd_sweep() {
    local hours=${1:-$SWEEP_HOURS_DEFAULT}
    sweep_instances "$(($(date -u +%s) - hours * 3600))"
    sweep_orphans
}

[ $# -ge 1 ] || usage
cmd=$1
shift
case "$cmd" in
create | info | destroy) [ $# -eq 1 ] || usage ;;
ssh) [ $# -ge 1 ] || usage ;;
sweep) [ $# -le 1 ] || usage ;;
*) usage ;;
esac
case "$cmd" in
create) cmd_create "$@" ;;
info) cmd_info "$@" ;;
ssh) cmd_ssh "$@" ;;
destroy) cmd_destroy "$@" ;;
sweep) cmd_sweep "$@" ;;
esac
