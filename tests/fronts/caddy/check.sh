#!/bin/sh
set -eu

recorded=/var/lib/ocel-front/caddy/config.sum
[ -f "$recorded" ] || {
    echo "check.sh: $recorded is missing, so up.sh never ran on this box" >&2
    exit 1
}

if ! sha256sum --check --status "$recorded"; then
    echo "check.sh: /etc/caddy/Caddyfile changed after up.sh wrote it, and ocel writes ocel.caddy and nothing else" >&2
    exit 1
fi

if ! systemctl is-active --quiet caddy; then
    echo "check.sh: caddy is not running" >&2
    exit 1
fi

if [ "$(curl -sk -m 10 --resolve mine.localhost:443:127.0.0.1 https://mine.localhost/)" != mine ]; then
    echo "check.sh: mine.localhost no longer answers from the site in your Caddyfile" >&2
    exit 1
fi
