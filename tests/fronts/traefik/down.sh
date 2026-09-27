#!/bin/sh
set -eu

docker rm -f ocel-front-traefik ocel-front-pebble ocel-front-mine >/dev/null 2>&1 || true
rm -rf /etc/ocel-front-traefik /var/lib/ocel-front/traefik
