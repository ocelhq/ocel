#!/bin/sh
set -eu

docker rm -f ocel-switchboard >/dev/null 2>&1 || true
rm -f /data/coolify/proxy/dynamic/ocel.yml
rm -rf /var/lib/ocel-front/coolify-traefik
