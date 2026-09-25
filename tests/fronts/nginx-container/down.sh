#!/bin/sh
set -eu

conf=/etc/ocel-front-container
if [ -f "$conf/compose.yaml" ]; then
    docker compose -f "$conf/compose.yaml" down >/dev/null 2>&1 || true
fi
docker rm -f ocel-front-nginx >/dev/null 2>&1 || true
docker network rm ocel >/dev/null 2>&1 || true
rm -rf "$conf" /var/lib/ocel-front/nginx-container
