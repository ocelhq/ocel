#!/bin/sh
set -eu

recorded=/var/lib/ocel-front/caddy-container/config.sum
[ -f "$recorded" ] || {
    echo "check.sh: $recorded is missing, so up.sh never ran on this box" >&2
    exit 1
}

if ! sha256sum --check --status "$recorded"; then
    echo "check.sh: /etc/ocel-front-caddy/Caddyfile changed after up.sh wrote it, and ocel writes ocel.caddy and nothing else" >&2
    exit 1
fi

if [ "$(docker inspect --format '{{.State.Status}}' ocel-front-caddy 2>/dev/null)" != running ]; then
    echo "check.sh: the ocel-front-caddy container is not running" >&2
    exit 1
fi
networks=$(docker inspect --format '{{range $name, $_ := .NetworkSettings.Networks}}{{$name}} {{end}}' ocel-front-caddy)
if [ "$networks" != "ocel-front-caddy " ]; then
    echo "check.sh: ocel-front-caddy sits on $networks, want ocel-front-caddy alone: ocel never moves a proxy it does not run" >&2
    exit 1
fi

if [ "$(curl -sk -m 10 --resolve mine.localhost:443:127.0.0.1 https://mine.localhost/)" != mine ]; then
    echo "check.sh: mine.localhost no longer answers from the site in your Caddyfile" >&2
    exit 1
fi
