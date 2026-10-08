#!/bin/sh
set -eu
umask 077

usage() {
	echo "usage: releases <project>/<app> promote <tier> <ref> | <project>/<app> forget <tier> | <project>/<app> reconcile <repository>" >&2
	exit 2
}

abort() {
	echo "releases: $1" >&2
	exit 2
}

reached() {
	local p
	p=$1
	while [ "${p%/*}" != "$p" ]; do
		p=${p%/*}
		[ -n "$p" ] || return 0
		if [ -d "$p" ] && [ ! -x "$p" ]; then
			abort "$p: Permission denied to $(id -un)"
		fi
	done
}

keep=3

[ $# -ge 2 ] || usage
scope=$1
verb=$2
shift 2

case $scope in
/* | */ | */*/* | *[!a-z0-9/-]*) abort "$scope is not a <project>/<app> scope" ;;
*/*) ;;
*) abort "$scope is not a <project>/<app> scope" ;;
esac
project=${scope%/*}
app=${scope#*/}

root="${OCEL_RELEASES_ROOT:-/var/lib/ocel/releases}"
reached "$root"
[ -d "$root" ] || abort "$root is missing; run ocel bootstrap"

dir="$root/$project/$app"
dropped="$dir/.dropped"

lock() {
	while :; do
		mkdir -p "$dir"
		exec 9<"$dir"
		flock -x 9
		[ "$(stat -c %i "$dir" 2>/dev/null)" = "$(stat -L -c %i /proc/self/fd/9)" ] && return
		exec 9<&-
	done
}

remove_emptied_scope() {
	if [ -z "$(find "$dir" -type f)" ]; then
		rmdir "$dir" 2>/dev/null || true
		rmdir "$root/$project" 2>/dev/null || true
	fi
}

coordinate() {
	case $1 in
	'' | -* | *[!A-Za-z0-9._:/@-]*) abort "$1 is not a valid image coordinate" ;;
	esac
}

scratch="$root/.staging.$$"
clean() {
	rm -f "$scratch".desired "$scratch".actual "$scratch".running "$scratch".going "$scratch".staged "$scratch".kept "$scratch".unnamed
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
	tier=$1
	ref=$2
	case $tier in
	'' | *[!a-z0-9-]*) abort "$tier is not a valid tier" ;;
	esac
	coordinate "$ref"
	lock
	file="$dir/$tier"
	: >>"$file"
	{
		printf '%s\n' "$ref"
		grep -F -x -v -e "$ref" "$file" || true
	} >"$scratch".staged
	tail -n +$((keep + 1)) "$scratch".staged >>"$dropped"
	head -n $keep "$scratch".staged >"$scratch".kept
	mv -f "$scratch".kept "$file"
	[ -s "$dropped" ] || rm -f "$dropped"
	;;
forget)
	[ $# -eq 1 ] || usage
	tier=$1
	case $tier in
	'' | *[!a-z0-9-]*) abort "$tier is not a valid tier" ;;
	esac
	lock
	if [ -f "$dir/$tier" ]; then
		cat "$dir/$tier" >>"$dropped"
	fi
	rm -f "$dir/$tier"
	[ -s "$dropped" ] || rm -f "$dropped"
	remove_emptied_scope
	;;
reconcile)
	[ $# -eq 1 ] || usage
	repository=$1
	coordinate "$repository"
	lock

	: >"$scratch".desired
	find "$dir" -type f ! -name '.*' -exec cat {} + >>"$scratch".desired

	docker ps --filter "label=ocel.app=$app" --filter "label=ocel.project=$project" --format '{{.Label "ocel.ref"}}' >"$scratch".running
	while IFS= read -r running; do
		[ -n "$running" ] || abort "a container with ocel.project=$project and ocel.app=$app has no ocel.ref"
		printf '%s\n' "$running" >>"$scratch".desired
	done <"$scratch".running

	docker images --filter "reference=$repository:*" --format '{{.Repository}}:{{.Tag}}' >"$scratch".actual
	grep -F -x -v -f "$scratch".desired "$scratch".actual >"$scratch".going || true

	while IFS= read -r going; do
		case $going in '' | *'<none>'*) continue ;; esac
		docker rmi "$going" >/dev/null 2>&1 || continue
		printf '%s\n' "$going"
	done <"$scratch".going

	if [ -f "$dropped" ]; then
		grep -F -x -v -f "$scratch".desired "$dropped" | sort -u >"$scratch".unnamed || true
		while IFS= read -r going; do
			[ -n "$going" ] || continue
			grep -F -x -q -e "$going" "$scratch".actual && continue
			printf '%s\n' "$going"
		done <"$scratch".unnamed
		rm -f "$dropped"
	fi
	remove_emptied_scope
	;;
*)
	usage
	;;
esac
