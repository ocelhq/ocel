#!/bin/sh
set -eu

docker rm -f ocel-switchboard >/dev/null 2>&1 || true
rm -f /data/coolify/proxy/caddy/dynamic/ocel.caddy
docker exec coolify-proxy caddy reload --config /config/caddy/Caddyfile.autosave >/dev/null 2>&1 || true
rm -rf /var/lib/ocel-front/coolify-caddy
