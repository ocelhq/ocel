#!/bin/sh
set -eu

conf=/etc/ocel-front-network
if [ -f "$conf/compose.yaml" ]; then
    docker compose -f "$conf/compose.yaml" down >/dev/null 2>&1 || true
fi
docker rm -f ocel-front-nginx-network >/dev/null 2>&1 || true
docker network rm ocel-front >/dev/null 2>&1 || true
rm -rf "$conf" /var/lib/ocel-front/nginx-network
