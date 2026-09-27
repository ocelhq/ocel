#!/bin/sh
set -eu

dir=/data/coolify/proxy/dynamic
recorded=/var/lib/ocel-front/coolify-traefik/config.sum
[ -f "$recorded" ] || {
    echo "check.sh: $recorded is missing, so up.sh never ran on this box" >&2
    exit 1
}

now=$(mktemp)
trap 'rm -f "$now"' EXIT
find "$dir" -type f ! -name ocel.yml | LC_ALL=C sort | xargs sha256sum > "$now"
if ! diff -u "$recorded" "$now" >&2; then
    echo "check.sh: Coolify's files in $dir changed, and ocel writes ocel.yml there and nothing else" >&2
    exit 1
fi

if ! curl -sk -m 10 --resolve web.p1305.test:443:127.0.0.1 https://web.p1305.test/ | grep -q '^Hostname:'; then
    echo "check.sh: web.p1305.test no longer reaches the Coolify app its labels route it to" >&2
    exit 1
fi
