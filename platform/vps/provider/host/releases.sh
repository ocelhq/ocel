#!/bin/sh
set -eu
umask 077

usage() {
	echo "usage: releases <project>/<app> promote <class> <ref> | <project>/<app> forget <class> | <project>/<app> reconcile <repository>" >&2
	exit 2
}

abort() {
	echo "releases: $1" >&2
	exit 2
}

keep=3

[ $# -ge 2 ] || usage
scope=$1
verb=$2
shift 2

case $scope in
/* | */ | */*/* | *[!a-z0-9/-]*) abort "$scope is no project and app this host keeps releases for" ;;
*/*) ;;
*) abort "$scope is no project and app this host keeps releases for" ;;
esac
project=${scope%/*}
app=${scope#*/}

root="${OCEL_RELEASES_ROOT:-/var/lib/ocel/releases}"
[ -d "$root" ] || abort "$root stands as no release window, and ocel bootstrap is what writes one"

hold() {
	mkdir -p "$root/$project/$app"
	exec 9<"$root/$project/$app"
	flock -x 9
}

coordinate() {
	case $1 in
	'' | -* | *[!A-Za-z0-9._:/@-]*) abort "$1 is no image coordinate ocel ever wrote" ;;
	esac
}

scratch="$root/.staging.$$"
clean() {
	rm -f "$scratch".desired "$scratch".actual "$scratch".running "$scratch".going "$scratch".staged
}
trap clean EXIT
trap 'clean; exit 129' HUP
trap 'clean; exit 130' INT
trap 'clean; exit 143' TERM

for stale in "$root"/.staging.*; do
	[ -e "$stale" ] || continue
	who=${stale#"$root"/.staging.}
	who=${who%.*}
	case $who in '' | *[!0-9]*) continue ;; esac
	if kill -0 "$who" 2>/dev/null; then continue; fi
	rm -f "$stale"
done

case "$verb" in
promote)
	[ $# -eq 2 ] || usage
	class=$1
	ref=$2
	case $class in
	'' | *[!a-z0-9-]*) abort "$class is no class this host serves" ;;
	esac
	coordinate "$ref"
	hold
	file="$root/$project/$app/$class"
	: >>"$file"
	{
		printf '%s\n' "$ref"
		grep -F -x -v -e "$ref" "$file" || true
	} | head -n $keep >"$scratch".staged
	mv -f "$scratch".staged "$file"
	;;
forget)
	[ $# -eq 1 ] || usage
	class=$1
	case $class in
	'' | *[!a-z0-9-]*) abort "$class is no class this host serves" ;;
	esac
	hold
	rm -f "$root/$project/$app/$class"
	rmdir "$root/$project/$app" 2>/dev/null || true
	rmdir "$root/$project" 2>/dev/null || true
	;;
reconcile)
	[ $# -eq 1 ] || usage
	repository=$1
	coordinate "$repository"

	: >"$scratch".desired
	if [ -d "$root/$project/$app" ]; then
		find "$root/$project/$app" -type f -exec cat {} + >>"$scratch".desired
	fi

	docker ps --filter "label=ocel.app=$app" --filter "label=ocel.project=$project" --format '{{.Label "ocel.ref"}}' >"$scratch".running
	while IFS= read -r running; do
		[ -n "$running" ] || abort "a container on this host carries ocel.project=$project and ocel.app=$app and names no ocel.ref, and ocel removes nothing while it cannot say what is running"
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
