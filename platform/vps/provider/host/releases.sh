#!/bin/sh
set -eu
umask 077

usage() {
	echo "usage: releases <project>/<app> promote <tier> <ref> | <project>/<app> forget <tier> | <project>/<app> reconcile <repository> | <project>/<app> settle <ref>... | <project>/<app> claimed <ref>..." >&2
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
	tries=0
	while :; do
		tries=$((tries + 1))
		if [ "$tries" -gt 50 ]; then
			mkdir -p "$dir"
			command exec 9<"$dir"
			abort "$dir kept vanishing before it could be locked"
		fi
		mkdir -p "$dir" 2>/dev/null || continue
		command exec 9<"$dir" 2>/dev/null || continue
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

read_desired() {
	: >"$scratch".desired
	find "$dir" -type f ! -name '.*' -exec cat {} + >>"$scratch".desired

	docker ps --filter "label=ocel.app=$app" --filter "label=ocel.project=$project" --format '{{.Label "ocel.ref"}}' >"$scratch".running
	while IFS= read -r running; do
		[ -n "$running" ] || abort "a container with ocel.project=$project and ocel.app=$app has no ocel.ref"
		printf '%s\n' "$running" >>"$scratch".desired
	done <"$scratch".running
}

coordinate() {
	case $1 in
	'' | -* | *[!A-Za-z0-9._:/@-]*) abort "$1 is not a valid image coordinate" ;;
	esac
}

scratch="$root/.staging.$$"
clean() {
	rm -f "$scratch".desired "$scratch".actual "$scratch".running "$scratch".going "$scratch".staged "$scratch".kept "$scratch".unnamed \
		"$scratch".removed "$scratch".pending "$scratch".settled
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
	read_desired

	docker images --filter "reference=$repository:*" --format '{{.Repository}}:{{.Tag}}' >"$scratch".actual
	grep -F -x -v -f "$scratch".desired "$scratch".actual >"$scratch".going || true

	: >"$scratch".removed
	while IFS= read -r going; do
		case $going in '' | *'<none>'*) continue ;; esac
		docker rmi "$going" >/dev/null 2>&1 || continue
		printf '%s\n' "$going" >>"$scratch".removed
		printf 'removed %s\n' "$going"
	done <"$scratch".going

	: >>"$dropped"
	cat "$dropped" "$scratch".removed | grep -F -x -v -f "$scratch".desired | sort -u >"$scratch".unnamed || true
	: >"$scratch".pending
	while IFS= read -r going; do
		[ -n "$going" ] || continue
		if grep -F -x -q -e "$going" "$scratch".actual && ! grep -F -x -q -e "$going" "$scratch".removed; then
			continue
		fi
		printf '%s\n' "$going" >>"$scratch".pending
		printf 'unused %s\n' "$going"
	done <"$scratch".unnamed
	mv -f "$scratch".pending "$dropped"
	[ -s "$dropped" ] || rm -f "$dropped"
	remove_emptied_scope
	;;
settle)
	[ $# -ge 1 ] || usage
	for ref in "$@"; do
		coordinate "$ref"
	done
	lock
	if [ -f "$dropped" ]; then
		printf '%s\n' "$@" >"$scratch".settled
		grep -F -x -v -f "$scratch".settled "$dropped" >"$scratch".kept || true
		mv -f "$scratch".kept "$dropped"
		[ -s "$dropped" ] || rm -f "$dropped"
	fi
	remove_emptied_scope
	;;
claimed)
	[ $# -ge 1 ] || usage
	for ref in "$@"; do
		coordinate "$ref"
	done
	lock
	read_desired
	for ref in "$@"; do
		if grep -F -x -q -e "$ref" "$scratch".desired; then
			printf '%s\n' "$ref"
		fi
	done
	remove_emptied_scope
	;;
*)
	usage
	;;
esac
