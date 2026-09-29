#!/bin/sh
set -eu

dir=/data/coolify/proxy/caddy/dynamic
recorded=/var/lib/ocel-front/coolify-caddy/config.sum
[ -f "$recorded" ] || {
    echo "check.sh: $recorded is missing, so up.sh never ran on this box" >&2
    exit 1
}

now=$(mktemp)
trap 'rm -f "$now"' EXIT
find "$dir" -type f ! -name ocel.caddy | LC_ALL=C sort | xargs -r sha256sum > "$now"
if ! diff -u "$recorded" "$now" >&2; then
    echo "check.sh: Coolify's files in $dir changed, and ocel writes ocel.caddy there and nothing else" >&2
    exit 1
fi

if [ "$(docker inspect --format '{{.State.Status}}' coolify-proxy 2>/dev/null)" != running ]; then
    echo "check.sh: coolify-proxy is not running" >&2
    exit 1
fi
if ! docker exec coolify-proxy wget -qO- http://127.0.0.1:2019/config/apps/http/servers | grep -q '"host"'; then
    echo "check.sh: coolify-proxy serves no site of Coolify's any more, as it does when an invalid import drops them all" >&2
    exit 1
fi
