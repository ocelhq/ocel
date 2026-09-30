#!/bin/sh
set -eu

image=mirror.gcr.io/library/caddy:2.11
network=ocel-front-caddy
conf=/etc/ocel-front-caddy
state=/var/lib/ocel-front/caddy-container

if ! command -v docker >/dev/null 2>&1; then
    curl -fsSL --retry 5 --retry-delay 2 https://get.docker.com | sh >/dev/null
fi
docker network inspect "$network" >/dev/null 2>&1 || docker network create "$network" >/dev/null

install -d -m 0755 "$conf" "$conf/ocel.d"
cat > "$conf/Caddyfile" <<EOF
{
	local_certs
	skip_install_trust
}

import $conf/ocel.d/*.caddy

mine.localhost {
	respond "mine"
}
EOF

docker rm -f ocel-front-caddy >/dev/null 2>&1 || true
docker run --detach --name ocel-front-caddy --restart unless-stopped \
    --network "$network" \
    --publish 80:80 --publish 443:443 \
    --volume "$conf/Caddyfile:/etc/caddy/Caddyfile:ro" \
    --volume "$conf/ocel.d:$conf/ocel.d:ro" \
    "$image" >/dev/null

at=0
until docker exec ocel-front-caddy wget -qO- http://127.0.0.1:2019/config/ >/dev/null 2>&1; do
    at=$((at + 1))
    [ "$at" -lt 30 ] || {
        echo "up.sh: ocel-front-caddy never answered on its admin endpoint" >&2
        docker logs --tail 5 ocel-front-caddy >&2 || true
        exit 1
    }
    sleep 1
done

install -d -m 0755 "$state"
sha256sum "$conf/Caddyfile" > "$state/config.sum"
