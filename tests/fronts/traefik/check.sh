#!/bin/sh
set -eu

conf=/etc/ocel-front-traefik
recorded=/var/lib/ocel-front/traefik/config.sum
[ -f "$recorded" ] || {
    echo "check.sh: $recorded is missing, so up.sh never ran on this box" >&2
    exit 1
}

now=$(mktemp)
trap 'rm -f "$now"' EXIT
find "$conf" -type f ! -path "$conf/acme/*" | LC_ALL=C sort | xargs sha256sum > "$now"
if ! diff -u "$recorded" "$now" >&2; then
    echo "check.sh: $conf differs from what up.sh wrote: ocel writes its own ocel.yml there, takes it out when it goes, and touches nothing else" >&2
    exit 1
fi

if [ "$(docker inspect --format '{{.State.Status}}' ocel-front-traefik 2>/dev/null)" != running ]; then
    echo "check.sh: the ocel-front-traefik container is not running" >&2
    exit 1
fi
if ! curl -sk -m 10 --resolve mine.p1306.test:443:127.0.0.1 https://mine.p1306.test/ | grep -q 'Welcome to nginx'; then
    echo "check.sh: mine.p1306.test no longer reaches the app your own router sends it to" >&2
    exit 1
fi
