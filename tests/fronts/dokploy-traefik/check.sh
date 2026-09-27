#!/bin/sh
set -eu

dir=/etc/dokploy/traefik/dynamic
recorded=/var/lib/ocel-front/dokploy-traefik/config.sum
[ -f "$recorded" ] || {
    echo "check.sh: $recorded is missing, so up.sh never ran on this box" >&2
    exit 1
}

now=$(mktemp)
trap 'rm -f "$now"' EXIT
find "$dir" -maxdepth 1 -type f -name '*.yml' ! -name ocel.yml | LC_ALL=C sort | xargs sha256sum > "$now"
if ! diff -u "$recorded" "$now" >&2; then
    echo "check.sh: Dokploy's files in $dir changed, and ocel writes ocel.yml there and nothing else" >&2
    exit 1
fi

if ! curl -sk -m 10 --resolve dapp.p1305.test:443:127.0.0.1 https://dapp.p1305.test/ | grep -q '^Hostname:'; then
    echo "check.sh: dapp.p1305.test no longer reaches the Dokploy application its file routes it to" >&2
    exit 1
fi
