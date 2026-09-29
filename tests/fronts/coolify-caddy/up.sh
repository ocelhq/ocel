#!/bin/sh
set -eu

dir=/data/coolify/proxy/caddy/dynamic
state=/var/lib/ocel-front/coolify-caddy

docker inspect --type container coolify >/dev/null 2>&1 || {
    echo "up.sh: this box runs no Coolify; clone the #1305 Coolify box switched to Caddy" >&2
    exit 1
}
case "$(docker inspect --format '{{.Config.Image}}' coolify-proxy 2>/dev/null)" in
*caddy-docker-proxy*) ;;
*)
    echo "up.sh: coolify-proxy runs no caddy-docker-proxy; switch the box's proxy to Caddy in Coolify first" >&2
    exit 1
    ;;
esac

docker rm -f ocel-switchboard >/dev/null 2>&1 || true
rm -f "$dir/ocel.caddy"

if [ "$(docker inspect --format '{{.State.Status}}' coolify-proxy)" != running ]; then
    docker compose --project-directory /data/coolify/proxy -f /data/coolify/proxy/docker-compose.yml up -d --wait --remove-orphans >/dev/null
fi
docker exec coolify-proxy caddy reload --config /config/caddy/Caddyfile.autosave >/dev/null 2>&1

install -d -m 0755 "$state"
find "$dir" -type f ! -name ocel.caddy | LC_ALL=C sort | xargs -r sha256sum > "$state/config.sum"
