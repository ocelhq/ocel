#!/usr/bin/env bash
set -euo pipefail

# A stand-in for scripts/incus.sh when no incus socket is reachable. Speaks the
# same OCEL_INCUS_{NAME,ADDR,USER,KEY} contract, so run.sh cannot tell them
# apart. Lower fidelity than a VM: alpine, no systemd, no cloud-init.

NAME=ocel-proto-box
STATE_DIR="${OCEL_INCUS_STATE:-${XDG_STATE_HOME:-$HOME/.local/state}/ocel-incus}"
KEY="$STATE_DIR/id_ed25519"

ensure_key() {
    mkdir -p "$STATE_DIR"
    [ -f "$KEY" ] || ssh-keygen -q -t ed25519 -f "$KEY" -N '' -C ocel-incus
}

up() {
    ensure_key
    docker rm -f "$NAME" >/dev/null 2>&1 || true
    docker run -d --privileged --name "$NAME" docker:28-dind >/dev/null

    local deadline=$((SECONDS + 90))
    until docker exec "$NAME" docker info >/dev/null 2>&1; do
        [ "$SECONDS" -lt "$deadline" ] || { echo "standin-box: dockerd never came up" >&2; exit 1; }
        sleep 1
    done

    docker exec "$NAME" apk add --no-cache openssh curl >/dev/null
    docker exec "$NAME" ssh-keygen -A >/dev/null
    docker exec "$NAME" sh -c 'adduser -D -h /var/lib/ocel -s /bin/sh ocel-deploy 2>/dev/null; addgroup ocel-deploy docker'
    docker exec "$NAME" sed -i 's/^ocel-deploy:!:/ocel-deploy:*:/' /etc/shadow
    docker exec "$NAME" sh -c 'mkdir -p /var/lib/ocel/.ssh && chmod 700 /var/lib/ocel/.ssh'
    docker exec -i "$NAME" sh -c 'cat > /var/lib/ocel/.ssh/authorized_keys' <"$KEY.pub"
    docker exec "$NAME" sh -c 'chown -R ocel-deploy:ocel-deploy /var/lib/ocel/.ssh && chmod 600 /var/lib/ocel/.ssh/authorized_keys'
    docker exec "$NAME" sh -c '/usr/sbin/sshd'

    local addr
    addr=$(docker inspect -f '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' "$NAME")
    printf 'OCEL_INCUS_NAME=%s\n' "$NAME"
    printf 'OCEL_INCUS_ADDR=%s\n' "$addr"
    printf 'OCEL_INCUS_USER=%s\n' ocel-deploy
    printf 'OCEL_INCUS_KEY=%s\n' "$KEY"
}

case "${1:-}" in
up) up ;;
down) docker rm -f "$NAME" >/dev/null 2>&1 || true ;;
*) echo "usage: standin-box.sh up|down" >&2; exit 2 ;;
esac
