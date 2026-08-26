#!/usr/bin/env bash

PROXY_IMAGE=basecamp/kamal-proxy:v0.9.2

proxy_up() {
    box "docker network create $NET >/dev/null 2>&1 || true"
    box "docker rm -f kamal-proxy >/dev/null 2>&1 || true"
    box "docker run -d --name kamal-proxy --network $NET --restart unless-stopped \
        -p 80:80 $PROXY_IMAGE" >/dev/null
    note "kamal-proxy resident on :80"
}

proxy_flip() {
    local name=$1
    box "docker exec kamal-proxy kamal-proxy deploy $APP \
        --target $name:$PORT --health-check-path / --drain-timeout 10s"
}

proxy_url() { printf 'http://%s/\n' "$OCEL_INCUS_ADDR"; }
