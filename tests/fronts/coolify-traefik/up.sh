#!/bin/sh
set -eu

dir=/data/coolify/proxy/dynamic
state=/var/lib/ocel-front/coolify-traefik
addr=$(ip -4 -o addr show scope global | awk '$2 !~ /^(docker|br-|veth)/ { split($4, at, "/"); print at[1]; exit }')

docker inspect --type container coolify >/dev/null 2>&1 || {
    echo "up.sh: this box runs no Coolify; clone the #1305 Coolify box" >&2
    exit 1
}

docker rm -f ocel-switchboard >/dev/null 2>&1 || true
rm -f "$dir/ocel.yml"

if [ "$(docker inspect --format '{{index .Config.Cmd 1}}' challtestsrv)" != "$addr" ]; then
    image=$(docker inspect --format '{{.Config.Image}}' challtestsrv)
    docker rm -f challtestsrv >/dev/null
    docker run --detach --name challtestsrv --network coolify --restart unless-stopped "$image" \
        -defaultIPv4 "$addr" -defaultIPv6 "" -http01 "" -https01 "" -tlsalpn01 "" -doh "" >/dev/null
fi

if [ "$(docker inspect --format '{{.State.Status}}' coolify-proxy 2>/dev/null)" != running ]; then
    docker compose --project-directory /data/coolify/proxy -f /data/coolify/proxy/docker-compose.yml up -d --wait --remove-orphans >/dev/null
fi

install -d -m 0755 "$state"
find "$dir" -type f ! -name ocel.yml | LC_ALL=C sort | xargs sha256sum > "$state/config.sum"
