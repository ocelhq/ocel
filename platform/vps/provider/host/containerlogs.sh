#!/bin/sh
since=$1
shift

follow() {
	from=$2
	while :; do
		started=$(date -u +%Y-%m-%dT%H:%M:%SZ)
		docker logs --follow --timestamps --since "$from" "$1" || exit
		from=$started
		sleep 1
	done
}

tag() {
	while IFS= read -r line || [ -n "$line" ]; do
		printf '%s %s\n' "$1" "$line"
	done
}

index=0
for name in "$@"; do
	{ { follow "$name" "$since" 2>&1 1>&3 3>&-; } | tag "$index" >&2 3>&-; } 3>&1 | tag "$index" &
	index=$((index + 1))
done
cat >/dev/null
kill 0
