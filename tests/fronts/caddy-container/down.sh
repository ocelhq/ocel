#!/bin/sh
set -eu

docker rm -f ocel-front-caddy >/dev/null 2>&1 || true
docker network rm ocel-front-caddy >/dev/null 2>&1 || true
rm -rf /etc/ocel-front-caddy /var/lib/ocel-front/caddy-container
