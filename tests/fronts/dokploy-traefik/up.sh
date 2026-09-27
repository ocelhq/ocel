#!/bin/sh
set -eu

dir=/etc/dokploy/traefik/dynamic
static=/etc/dokploy/traefik/traefik.yml
state=/var/lib/ocel-front/dokploy-traefik
addr=$(ip -4 -o addr show scope global | awk '$2 !~ /^(docker|br-|veth)/ { split($4, at, "/"); print at[1]; exit }')

docker service inspect dokploy >/dev/null 2>&1 || {
    echo "up.sh: this box runs no Dokploy; clone the #1305 Dokploy box" >&2
    exit 1
}

docker rm -f ocel-switchboard >/dev/null 2>&1 || true
rm -f "$dir/ocel.yml"

if [ "$(docker inspect --format '{{index .Config.Cmd 1}}' challtestsrv)" != "$addr" ]; then
    image=$(docker inspect --format '{{.Config.Image}}' challtestsrv)
    docker rm -f challtestsrv >/dev/null
    docker run --detach --name challtestsrv --network pebble --restart unless-stopped "$image" \
        -defaultIPv4 "$addr" -defaultIPv6 "" >/dev/null
fi
if ! grep -q "caServer: https://$addr:14000/dir" "$static"; then
    sed -i "s#caServer: https://[0-9.]*:14000/dir#caServer: https://$addr:14000/dir#" "$static"
    docker restart dokploy-traefik >/dev/null
fi

install -d -m 0755 "$state"
find "$dir" -maxdepth 1 -type f -name '*.yml' ! -name ocel.yml | LC_ALL=C sort | xargs sha256sum > "$state/config.sum"
