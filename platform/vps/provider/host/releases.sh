#!/bin/sh
set -eu
umask 077

usage() {
	echo "usage: releases <app> promote <class> <ref> | <app> reconcile <repository>" >&2
	exit 2
}

abort() {
	echo "releases: $1" >&2
	exit 2
}

keep=3

[ $# -ge 2 ] || usage
app=$1
verb=$2
shift 2

case $app in
'' | *[!a-z0-9-]*) abort "$app is no app this host keeps releases for" ;;
esac

root="${OCEL_RELEASES_ROOT:-/var/lib/ocel/releases}"
[ -d "$root" ] || abort "$root stands as no release window, and ocel bootstrap is what writes one"

lock="$root/.lock"
: >>"$lock"
exec 9>"$lock"
flock -x 9

coordinate() {
	case $1 in
	'' | -* | *[!A-Za-z0-9._:/@-]*) abort "$1 is no image coordinate ocel ever wrote" ;;
	esac
}

scratch="$root/.staging.$$"
trap 'rm -f "$scratch".desired "$scratch".actual "$scratch".running "$scratch".going "$scratch".staged' EXIT

case "$verb" in
promote)
	[ $# -eq 2 ] || usage
	class=$1
	ref=$2
	case $class in
	'' | *[!a-z0-9-]*) abort "$class is no class this host serves" ;;
	esac
	coordinate "$ref"
	mkdir -p "$root/$app"
	file="$root/$app/$class"
	: >>"$file"
	{
		printf '%s\n' "$ref"
		grep -F -x -v -e "$ref" "$file" || true
	} | head -n $keep >"$scratch".staged
	mv -f "$scratch".staged "$file"
	;;
reconcile)
	[ $# -eq 1 ] || usage
	repository=$1
	coordinate "$repository"

	: >"$scratch".desired
	if [ -d "$root/$app" ]; then
		find "$root/$app" -type f -exec cat {} + >>"$scratch".desired
	fi

	docker ps --filter "label=ocel.app=$app" --format '{{.Label "ocel.ref"}}' >"$scratch".running
	while IFS= read -r running; do
		[ -n "$running" ] || abort "a container on this host carries ocel.app=$app and names no ocel.ref, and ocel removes nothing while it cannot say what is running"
		printf '%s\n' "$running" >>"$scratch".desired
	done <"$scratch".running

	docker images --filter "reference=$repository:*" --format '{{.Repository}}:{{.Tag}}' >"$scratch".actual
	grep -F -x -v -f "$scratch".desired "$scratch".actual >"$scratch".going || true

	while IFS= read -r going; do
		case $going in '' | *'<none>'*) continue ;; esac
		docker rmi "$going" >/dev/null 2>&1 || continue
		printf '%s\n' "$going"
	done <"$scratch".going
	;;
*)
	usage
	;;
esac
