#!/bin/sh
set -eu

conf=/etc/ocel-front-network
recorded=/var/lib/ocel-front/nginx-network/config.sum
[ -f "$recorded" ] || {
    echo "check.sh: $recorded is missing, so up.sh never ran on this box" >&2
    exit 1
}

now=$(mktemp)
trap 'rm -f "$now"' EXIT
find "$conf" -type f | LC_ALL=C sort | xargs sha256sum > "$now"
if ! diff -u "$recorded" "$now" >&2; then
    echo "check.sh: $conf changed after up.sh wrote it, and ocel writes nothing to a proxy it does not run" >&2
    exit 1
fi

if [ "$(docker inspect --format '{{.State.Status}}' ocel-front-nginx-network 2>/dev/null)" != running ]; then
    echo "check.sh: the ocel-front-nginx-network container is not running" >&2
    exit 1
fi
networks=$(docker inspect --format '{{range $name, $_ := .NetworkSettings.Networks}}{{$name}} {{end}}' ocel-front-nginx-network)
if [ "$networks" != "ocel-front " ]; then
    echo "check.sh: ocel-front-nginx-network sits on $networks, want ocel-front alone: ocel never moves a proxy it does not run" >&2
    exit 1
fi
