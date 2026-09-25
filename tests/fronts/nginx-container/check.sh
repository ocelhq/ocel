#!/bin/sh
set -eu

conf=/etc/ocel-front-container
held=/var/lib/ocel-front/nginx-container/config.sum
[ -f "$held" ] || {
    echo "check.sh: $held is missing, so up.sh never ran on this box" >&2
    exit 1
}

now=$(mktemp)
trap 'rm -f "$now"' EXIT
find "$conf" -type f | LC_ALL=C sort | xargs sha256sum > "$now"
if ! diff -u "$held" "$now" >&2; then
    echo "check.sh: $conf changed after up.sh wrote it, and ocel writes nothing to a proxy it does not run" >&2
    exit 1
fi

if [ "$(docker inspect --format '{{.State.Status}}' ocel-front-nginx 2>/dev/null)" != running ]; then
    echo "check.sh: the ocel-front-nginx container is not running" >&2
    exit 1
fi
if ! docker inspect --format '{{range $name, $_ := .NetworkSettings.Networks}}{{$name}} {{end}}' ocel-front-nginx | grep -qw ocel; then
    echo "check.sh: ocel-front-nginx left the ocel network it reaches the switchboard on" >&2
    exit 1
fi
