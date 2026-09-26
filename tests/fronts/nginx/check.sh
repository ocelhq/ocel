#!/bin/sh
set -eu

recorded=/var/lib/ocel-front/nginx/config.sum
[ -f "$recorded" ] || {
    echo "check.sh: $recorded is missing, so up.sh never ran on this box" >&2
    exit 1
}

now=$(mktemp)
trap 'rm -f "$now"' EXIT
find /etc/nginx -type f | LC_ALL=C sort | xargs sha256sum > "$now"
if ! diff -u "$recorded" "$now" >&2; then
    echo "check.sh: /etc/nginx changed after up.sh wrote it, and ocel writes nothing to a proxy it does not run" >&2
    exit 1
fi

if ! systemctl is-active --quiet nginx; then
    echo "check.sh: nginx is not running" >&2
    exit 1
fi
