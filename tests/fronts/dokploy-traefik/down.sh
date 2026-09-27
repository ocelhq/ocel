#!/bin/sh
set -eu

docker rm -f ocel-switchboard >/dev/null 2>&1 || true
rm -f /etc/dokploy/traefik/dynamic/ocel.yml
rm -rf /var/lib/ocel-front/dokploy-traefik
