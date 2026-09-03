#!/usr/bin/env bash
set -euo pipefail

STATE_ROOT="${OCEL_EC2_STATE:-${XDG_STATE_HOME:-$HOME/.local/state}/ocel-ec2}"
REGION="${AWS_REGION:-${AWS_DEFAULT_REGION:-us-east-1}}"
AMI_PARAM=/aws/service/canonical/ubuntu/server/24.04/stable/current/amd64/hvm/ebs-gp3/ami-id
INSTANCE_TYPE="${OCEL_EC2_INSTANCE_TYPE:-t3.small}"
SSH_USER=ubuntu
SSH_WAIT_SECS="${OCEL_EC2_SSH_WAIT:-300}"
ROOT_SIZE_GIB=20
LIVE_STATES=pending,running,shutting-down,stopping,stopped
SWEEP_HOURS_DEFAULT=3

usage() {
    cat <<'EOF'
usage: scripts/ec2.sh <command> [args]

  create <name>          launch an Ubuntu instance with a public IP, wait for
                         SSH, print info lines
  info <name>            print OCEL_EC2_{NAME,ADDR,USER,KEY,INSTANCE}= lines
                         (eval-able)
  ssh <name> [cmd...]    SSH into the instance
  destroy <name>         terminate the instance and delete its security group,
                         key pair and state, idempotent
  sweep [hours]          destroy every journey instance older than hours
                         (default 3), then the security groups and key pairs
                         nothing references
  run <name> -- cmd...   create, run cmd with OCEL_EC2_* exported,
                         destroy on exit no matter what

This spends the AWS account the environment is authenticated against.
EOF
    exit 2
}

die() {
    echo "ec2.sh: $*" >&2
    exit 1
}

box_dir() {
    printf '%s/%s\n' "$STATE_ROOT" "$1"
}

key_path() {
    printf '%s/id_ed25519\n' "$(box_dir "$1")"
}

resource_name() {
    printf 'ocel-ec2-%s\n' "$1"
}

meta_get() {
    local file
    file="$(box_dir "$1")/meta"
    [ -f "$file" ] || return 0
    sed -n "s/^$2=//p" "$file"
}

meta_set() {
    local name=$1 dir
    shift
    dir=$(box_dir "$name")
    mkdir -p "$dir"
    printf '%s\n' "$@" >"$dir/meta"
}

region_of() {
    local region
    region=$(meta_get "$1" region)
    printf '%s\n' "${region:-$REGION}"
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

instance_of() {
    local name=$1 id
    id=$(meta_get "$name" instance)
    if [ -n "$id" ]; then
        printf '%s\n' "$id"
        return 0
    fi
    aws ec2 describe-instances \
        --region "$(region_of "$name")" \
        --filters "Name=tag:ocel:name,Values=$name" "Name=instance-state-name,Values=$LIVE_STATES" \
        --query 'Reservations[].Instances[].InstanceId' \
        --output text | tr '\t' '\n' | sed '/^$/d' | head -n1
}

addr_of() {
    aws ec2 describe-instances \
        --region "$2" \
        --instance-ids "$1" \
        --query 'Reservations[0].Instances[0].PublicIpAddress' \
        --output text 2>/dev/null | sed 's/^None$//'
}

wait_ssh() {
    local name=$1 addr=$2 deadline=$((SECONDS + SSH_WAIT_SECS)) opts
    mapfile -t opts < <(ssh_opts "$(key_path "$name")")
    while [ "$SECONDS" -lt "$deadline" ]; do
        if ssh "${opts[@]}" -o ConnectTimeout=5 "$SSH_USER@$addr" true 2>/dev/null; then
            return 0
        fi
        sleep 5
    done
    diagnose_no_ssh "$name" "$addr" >&2
    die "$name: no SSH after ${SSH_WAIT_SECS}s"
}

diagnose_no_ssh() {
    local name=$1 addr=$2
    echo "ec2.sh: $addr answered no SSH, so the instance is still booting, the"
    echo "ec2.sh: security group does not let this network in, or cloud-init failed."
    echo "ec2.sh: read the boot log with:"
    echo "ec2.sh:   aws ec2 get-console-output --region $(region_of "$name") --instance-id $(instance_of "$name") --output text"
}

print_info() {
    local name=$1 addr=$2 instance=$3
    printf 'OCEL_EC2_NAME=%s\n' "$name"
    printf 'OCEL_EC2_ADDR=%s\n' "$addr"
    printf 'OCEL_EC2_USER=%s\n' "$SSH_USER"
    printf 'OCEL_EC2_KEY=%s\n' "$(key_path "$name")"
    printf 'OCEL_EC2_INSTANCE=%s\n' "$instance"
}

ensure_key_pair() {
    local name=$1 region=$2 pair
    pair=$(resource_name "$name")
    aws ec2 delete-key-pair --region "$region" --key-name "$pair" >/dev/null 2>&1 || true
    aws ec2 import-key-pair \
        --region "$region" \
        --key-name "$pair" \
        --public-key-material "fileb://$(key_path "$name").pub" >/dev/null
}

ensure_security_group() {
    local name=$1 region=$2 vpc=$3 group id
    group=$(resource_name "$name")
    id=$(aws ec2 describe-security-groups \
        --region "$region" \
        --filters "Name=group-name,Values=$group" "Name=vpc-id,Values=$vpc" \
        --query 'SecurityGroups[0].GroupId' \
        --output text 2>/dev/null | sed 's/^None$//')
    if [ -z "$id" ]; then
        id=$(aws ec2 create-security-group \
            --region "$region" \
            --group-name "$group" \
            --description "ocel journey box $name" \
            --vpc-id "$vpc" \
            --query GroupId \
            --output text)
        local port
        for port in 22 80 443; do
            aws ec2 authorize-security-group-ingress \
                --region "$region" \
                --group-id "$id" \
                --ip-permissions "IpProtocol=tcp,FromPort=$port,ToPort=$port,IpRanges=[{CidrIp=0.0.0.0/0}],Ipv6Ranges=[{CidrIpv6=::/0}]" >/dev/null
        done
    fi
    printf '%s\n' "$id"
}

cmd_create() {
    local name=$1 region ami vpc subnet sg instance addr created
    region=$REGION
    ensure_key "$name"
    trap 'discard_half_made "'"$name"'" $?' EXIT
    trap 'exit 130' INT
    trap 'exit 143' TERM
    meta_set "$name" "region=$region"
    ami=$(aws ssm get-parameter --region "$region" --name "$AMI_PARAM" --query Parameter.Value --output text)
    vpc=$(aws ec2 describe-vpcs --region "$region" --filters Name=isDefault,Values=true --query 'Vpcs[0].VpcId' --output text)
    [ -n "$vpc" ] && [ "$vpc" != None ] || die "$name: no default VPC in $region"
    subnet=$(aws ec2 describe-subnets \
        --region "$region" \
        --filters "Name=vpc-id,Values=$vpc" Name=default-for-az,Values=true \
        --query 'Subnets[0].SubnetId' \
        --output text)
    [ -n "$subnet" ] && [ "$subnet" != None ] || die "$name: no default subnet in $vpc"
    ensure_key_pair "$name" "$region"
    sg=$(ensure_security_group "$name" "$region" "$vpc")
    meta_set "$name" "region=$region" "sg=$sg"
    created=$(date -u +%Y-%m-%dT%H:%M:%SZ)
    local tags="{Key=Name,Value=$(resource_name "$name")},{Key=ocel:ec2,Value=journey},{Key=ocel:name,Value=$name},{Key=ocel:created-at,Value=$created}"
    cat >"$(box_dir "$name")/user-data.yaml" <<EOF
#cloud-config
ssh_authorized_keys:
  - $(cat "$(key_path "$name").pub")
EOF
    instance=$(aws ec2 run-instances \
        --region "$region" \
        --image-id "$ami" \
        --instance-type "$INSTANCE_TYPE" \
        --key-name "$(resource_name "$name")" \
        --subnet-id "$subnet" \
        --security-group-ids "$sg" \
        --associate-public-ip-address \
        --metadata-options HttpTokens=required,HttpEndpoint=enabled \
        --block-device-mappings "DeviceName=/dev/sda1,Ebs={VolumeSize=$ROOT_SIZE_GIB,VolumeType=gp3,DeleteOnTermination=true}" \
        --tag-specifications "ResourceType=instance,Tags=[$tags]" "ResourceType=volume,Tags=[$tags]" \
        --user-data "file://$(box_dir "$name")/user-data.yaml" \
        --query 'Instances[0].InstanceId' \
        --output text)
    meta_set "$name" "region=$region" "sg=$sg" "instance=$instance"
    aws ec2 wait instance-running --region "$region" --instance-ids "$instance"
    addr=$(addr_of "$instance" "$region")
    [ -n "$addr" ] || die "$name: running without a public IP"
    wait_ssh "$name" "$addr"
    trap - EXIT
    print_info "$name" "$addr" "$instance"
}

discard_half_made() {
    local name=$1 status=$2
    trap - EXIT
    [ "$status" -eq 0 ] && return 0
    if [ -n "${OCEL_EC2_KEEP:-}" ]; then
        echo "ec2.sh: leaving $name behind to inspect (OCEL_EC2_KEEP is set)" >&2
        return 0
    fi
    echo "ec2.sh: destroying half-made $name (OCEL_EC2_KEEP=1 keeps it)" >&2
    cmd_destroy "$name" >&2 || true
    return 0
}

cmd_info() {
    local name=$1 region instance addr
    region=$(region_of "$name")
    instance=$(instance_of "$name")
    [ -n "$instance" ] || die "$name: no instance (was it created?)"
    addr=$(addr_of "$instance" "$region")
    [ -n "$addr" ] || die "$name: no public IP (is it running?)"
    print_info "$name" "$addr" "$instance"
}

cmd_ssh() {
    local name=$1 addr instance opts
    shift
    instance=$(instance_of "$name")
    [ -n "$instance" ] || die "$name: no instance (was it created?)"
    addr=$(addr_of "$instance" "$(region_of "$name")")
    [ -n "$addr" ] || die "$name: no public IP (is it running?)"
    mapfile -t opts < <(ssh_opts "$(key_path "$name")")
    ssh "${opts[@]}" "$SSH_USER@$addr" "$@"
}

delete_security_group() {
    local region=$1 id=$2 deadline=$((SECONDS + 120))
    while :; do
        if aws ec2 delete-security-group --region "$region" --group-id "$id" 2>/dev/null; then
            return 0
        fi
        if ! aws ec2 describe-security-groups --region "$region" --group-ids "$id" >/dev/null 2>&1; then
            return 0
        fi
        [ "$SECONDS" -lt "$deadline" ] || die "security group $id is still in use after 120s"
        sleep 10
    done
}

cmd_destroy() {
    local name=$1 region sg instances
    region=$(region_of "$name")
    mapfile -t instances < <(aws ec2 describe-instances \
        --region "$region" \
        --filters "Name=tag:ocel:name,Values=$name" "Name=instance-state-name,Values=$LIVE_STATES" \
        --query 'Reservations[].Instances[].InstanceId' \
        --output text | tr '\t' '\n' | sed '/^$/d')
    if [ "${#instances[@]}" -gt 0 ]; then
        aws ec2 terminate-instances --region "$region" --instance-ids "${instances[@]}" >/dev/null
        aws ec2 wait instance-terminated --region "$region" --instance-ids "${instances[@]}"
    fi
    sg=$(meta_get "$name" sg)
    if [ -z "$sg" ]; then
        sg=$(aws ec2 describe-security-groups \
            --region "$region" \
            --filters "Name=group-name,Values=$(resource_name "$name")" \
            --query 'SecurityGroups[0].GroupId' \
            --output text 2>/dev/null | sed 's/^None$//')
    fi
    [ -n "$sg" ] && delete_security_group "$region" "$sg"
    aws ec2 delete-key-pair --region "$region" --key-name "$(resource_name "$name")" >/dev/null 2>&1 || true
    rm -rf "$(box_dir "$name")"
}

sweep_instances() {
    local region=$1 cutoff=$2 name created
    while IFS=$'\t' read -r name created; do
        [ -n "$name" ] || continue
        [ -n "$created" ] || continue
        [ "$(date -u -d "$created" +%s)" -lt "$cutoff" ] || continue
        cmd_destroy "$name"
        echo "ec2.sh: reclaimed instance $name (created $created)"
    done < <(aws ec2 describe-instances \
        --region "$region" \
        --filters Name=tag:ocel:ec2,Values=journey "Name=instance-state-name,Values=$LIVE_STATES" \
        --output json |
        jq -r '.Reservations[].Instances[].Tags | from_entries
               | [(.["ocel:name"] // ""), (.["ocel:created-at"] // "")] | @tsv' | sort -u)
}

sweep_orphans() {
    local region=$1 live_sgs live_keys id name
    live_sgs=$(aws ec2 describe-instances \
        --region "$region" \
        --filters "Name=instance-state-name,Values=$LIVE_STATES" \
        --query 'Reservations[].Instances[].SecurityGroups[].GroupId' \
        --output text | tr '\t' '\n' | sed '/^$/d')
    live_keys=$(aws ec2 describe-instances \
        --region "$region" \
        --filters "Name=instance-state-name,Values=$LIVE_STATES" \
        --query 'Reservations[].Instances[].KeyName' \
        --output text | tr '\t' '\n' | sed '/^$/d')
    while read -r id name; do
        [ -n "$id" ] || continue
        grep -qxF "$id" <<<"$live_sgs" && continue
        delete_security_group "$region" "$id"
        echo "ec2.sh: reclaimed security group $name"
    done < <(aws ec2 describe-security-groups \
        --region "$region" \
        --filters 'Name=group-name,Values=ocel-ec2-*' \
        --query 'SecurityGroups[].[GroupId,GroupName]' \
        --output text)
    while read -r name; do
        [ -n "$name" ] || continue
        grep -qxF "$name" <<<"$live_keys" && continue
        aws ec2 delete-key-pair --region "$region" --key-name "$name" >/dev/null
        echo "ec2.sh: reclaimed key pair $name"
    done < <(aws ec2 describe-key-pairs \
        --region "$region" \
        --filters 'Name=key-name,Values=ocel-ec2-*' \
        --query 'KeyPairs[].KeyName' \
        --output text | tr '\t' '\n' | sed '/^$/d')
}

cmd_sweep() {
    local hours=${1:-$SWEEP_HOURS_DEFAULT}
    sweep_instances "$REGION" "$(($(date -u +%s) - hours * 3600))"
    sweep_orphans "$REGION"
}

cmd_run() {
    local name=$1
    shift
    [ "${1:-}" = "--" ] || usage
    shift
    [ $# -gt 0 ] || usage
    trap 'cmd_destroy "'"$name"'" >&2 || true' EXIT
    trap 'exit 130' INT
    trap 'exit 143' TERM
    local addr
    addr=$(cmd_create "$name" | sed -n 's/^OCEL_EC2_ADDR=//p')
    [ -n "$addr" ] || die "$name: created without an address, so there is nothing to hand the command"
    OCEL_EC2_NAME=$name \
        OCEL_EC2_ADDR=$addr \
        OCEL_EC2_USER=$SSH_USER \
        OCEL_EC2_KEY=$(key_path "$name") \
        OCEL_EC2_INSTANCE=$(instance_of "$name") \
        "$@"
}

[ $# -ge 1 ] || usage
cmd=$1
shift
case "$cmd" in
create | info | destroy) [ $# -eq 1 ] || usage ;;
ssh | run) [ $# -ge 1 ] || usage ;;
sweep) [ $# -le 1 ] || usage ;;
*) usage ;;
esac
case "$cmd" in
create) cmd_create "$@" ;;
info) cmd_info "$@" ;;
ssh) cmd_ssh "$@" ;;
destroy) cmd_destroy "$@" ;;
sweep) cmd_sweep "$@" ;;
run) cmd_run "$@" ;;
esac
