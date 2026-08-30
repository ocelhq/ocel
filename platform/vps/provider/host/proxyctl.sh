#!/bin/sh
set -eu

socket=${OCEL_PROXY_ADMIN:-/run/caddy-admin.sock}

usage() {
	echo "usage: proxyctl gate <host:port> <path> <seconds>|flip <config>|upstreams" >&2
	exit 2
}

abort() {
	echo "proxyctl: $1" >&2
	exit 2
}

admin() {
	curl -sS --fail-with-body --unix-socket "$socket" "$@"
}

[ $# -ge 1 ] || usage
verb=$1
shift

case "$verb" in
gate)
	[ $# -eq 3 ] || usage
	code=$(curl -s -o /dev/null -m "$3" -w '%{http_code}' "http://$1$2" 2>/dev/null) || {
		echo "$1 never answered $2 within ${3}s" >&2
		exit 4
	}
	printf '%s\n' "$code"
	case "$code" in
	2??) ;;
	*) exit 3 ;;
	esac
	;;
flip)
	[ $# -eq 1 ] || usage
	[ -f "$1" ] || abort "$1 is no config on this host"
	grep -q "$socket" "$1" ||
		abort "$1 declares no admin endpoint at $socket, and caddy moves the admin endpoint before it validates the rest: a config without one takes the socket this helper is reached through with it and opens a tcp listener in its place"
	admin -X POST -H 'Content-Type: application/json' --data-binary @"$1" http://localhost/load
	;;
upstreams)
	[ $# -eq 0 ] || usage
	admin http://localhost/reverse_proxy/upstreams
	;;
*)
	usage
	;;
esac
