#!/usr/bin/env bash

PROXY_IMAGE=caddy:2.10-alpine

proxy_up() {
    box "docker network create $NET >/dev/null 2>&1 || true"
    box "docker rm -f caddy >/dev/null 2>&1 || true"
    box "printf '%s' '{\"admin\":{\"listen\":\"0.0.0.0:2019\"},\"apps\":{\"http\":{\"servers\":{\"ocel\":{\"listen\":[\":80\"],\"routes\":[{\"@id\":\"$APP\",\"handle\":[{\"handler\":\"reverse_proxy\",\"upstreams\":[{\"dial\":\"unset:$PORT\"}]}]}]}}}}}' > /tmp/caddy.json"
    box "docker run -d --name caddy --network $NET --restart unless-stopped \
        -p 80:80 -v /tmp/caddy.json:/etc/caddy.json:ro $PROXY_IMAGE \
        caddy run --config /etc/caddy.json" >/dev/null
    note "caddy resident on :80, admin API on the ocel network"
}

proxy_flip() {
    local name=$1
    box "docker run --rm --network $NET curlimages/curl:8.11.1 -sS -f \
        -X PATCH -H 'Content-Type: application/json' \
        -d '[{\"dial\":\"$name:$PORT\"}]' \
        http://caddy:2019/id/$APP/handle/0/upstreams"
}

proxy_url() { printf 'http://%s/\n' "$OCEL_INCUS_ADDR"; }
