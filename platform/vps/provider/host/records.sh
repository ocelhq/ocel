#!/bin/sh
set -eu
umask 077

usage() {
	echo "usage: records <tier> read|write|pair|remove|list [args]" >&2
	exit 2
}

abort() {
	echo "records: $1" >&2
	exit 2
}

[ $# -ge 2 ] || usage
tier=$1
verb=$2
shift 2

case $tier in
'' | *[!a-z0-9-]*) abort "$tier is not a valid tier" ;;
esac

dir="${OCEL_RECORDS_ROOT:-/var/lib/ocel}/$tier/records"
[ ! -L "$dir" ] || abort "$dir is a symlink to $(readlink "$dir"), not the directory ocel bootstrap made"
[ -d "$dir" ] || abort "$dir is missing; run ocel bootstrap"

ours() {
	if [ -L "$1" ]; then
		abort "$1 is a symlink"
	fi
	case $1 in
	*/../* | */..) abort "$1 climbs out of $dir" ;;
	esac
	case $1 in
	"$dir"/*) ;;
	*) abort "$1 is outside $dir" ;;
	esac
}

belongs() {
	[ "$(id -u)" = 0 ] || return 0
	local p
	p=$1
	while [ "$p" != "$dir" ]; do
		ours "$p"
		chown -h --reference="$dir" "$p" ||
			abort "could not hand $p to the owner of $dir"
		p=$(dirname "$p")
	done
}

lock="$dir/.lock"
ours "$lock"
: >>"$lock"
belongs "$lock"
exec 9>"$lock"
flock -x 9

fileof() { printf '%s/%s.json' "$dir" "$1"; }

revof() {
	line=$(head -n1 "$1")
	rest=${line#\{\"revision\":\"}
	[ "$rest" != "$line" ] || abort "$1 is not an entry ocel wrote"
	rev=${rest%%\"*}
	[ -n "$rev" ] || abort "$1 names no revision"
	printf '%s' "$rev"
}

valueof() {
	line=$(head -n1 "$1")
	rest=${line#*\",\"value\":}
	[ "$rest" != "$line" ] || abort "$1 is not an entry ocel wrote"
	printf '%s' "${rest%\}}"
}

refuseolder() {
	older="${1%.json}.rec"
	[ ! -f "$older" ] || abort "$older is an entry an older ocel wrote, in a layout this build does not read"
}

readrev() {
	current=
	if [ ! -f "$1" ]; then
		refuseolder "$1"
		return 0
	fi
	current=$(revof "$1")
}

mint() {
	rev=$(od -An -N16 -tx1 /dev/urandom | tr -d ' \n')
	[ ${#rev} -eq 32 ] || abort "could not mint a revision"
	case $rev in
	*[!0-9a-f]*) abort "could not mint a revision" ;;
	esac
}

checked() {
	[ -n "$1" ] || abort "an entry's value is empty"
}

stage() {
	staged="$1.staged"
	mkdir -p "$(dirname "$1")"
	ours "$staged"
	printf '{"revision":"%s","value":%s}\n' "$2" "$3" >"$staged"
	belongs "$staged"
}

prune() {
	d=$(dirname "$1")
	while [ "$d" != "$dir" ]; do
		rmdir "$d" 2>/dev/null || break
		d=$(dirname "$d")
	done
}

emit() {
	rev=$(revof "$2")
	value=$(valueof "$2")
	printf '%s\t%s\t%s\n' "$1" "$rev" "$value"
}

case "$verb" in
read)
	[ $# -eq 1 ] || usage
	f=$(fileof "$1")
	ours "$f"
	if [ ! -f "$f" ]; then
		refuseolder "$f"
		exit 3
	fi
	rev=$(revof "$f")
	value=$(valueof "$f")
	printf '%s\t%s\n' "$rev" "$value"
	;;
write)
	[ $# -eq 2 ] || usage
	f=$(fileof "$1")
	ours "$f"
	readrev "$f"
	[ "$current" = "$2" ] || exit 4
	IFS= read -r body || abort "a write sent no value"
	checked "$body"
	mint
	stage "$f" "$rev" "$body"
	mv -f "$staged" "$f"
	printf '%s\n' "$rev"
	;;
pair)
	[ $# -eq 4 ] || usage
	first=$(fileof "$1")
	second=$(fileof "$3")
	ours "$first"
	ours "$second"
	readrev "$first"
	[ "$current" = "$2" ] || exit 4
	readrev "$second"
	[ "$current" = "$4" ] || exit 4
	IFS= read -r one || abort "a pair sent one body, want two"
	IFS= read -r two || abort "a pair sent one body, want two"
	checked "$one"
	checked "$two"
	mint
	onerev=$rev
	mint
	tworev=$rev
	stage "$first" "$onerev" "$one"
	onestaged=$staged
	stage "$second" "$tworev" "$two"
	mv -f "$onestaged" "$first"
	mv -f "$staged" "$second"
	printf '%s\t%s\n' "$onerev" "$tworev"
	;;
remove)
	[ $# -eq 2 ] || usage
	f=$(fileof "$1")
	ours "$f"
	[ -f "$f" ] || exit 3
	readrev "$f"
	[ "$current" = "$2" ] || exit 4
	rm -f "$f"
	prune "$f"
	echo removed
	;;
list)
	[ $# -ge 1 ] && [ $# -le 2 ] || usage
	partition="$dir/$1"
	ours "$partition"
	under=$partition
	if [ $# -eq 2 ]; then
		under="$partition/$2"
		ours "$under"
	fi
	found="$dir/.list"
	ours "$found"
	: >"$found"
	belongs "$found"
	if [ "$under" != "$partition" ] && [ -f "$under.json" ]; then
		printf '%s\n' "$under.json" >>"$found"
	fi
	if [ -d "$under" ]; then
		find "$under" -type f -name '*.json' >>"$found"
	fi
	while IFS= read -r f; do
		name=${f#"$partition/"}
		emit "${name%.json}" "$f"
	done <"$found"
	rm -f "$found"
	;;
*)
	usage
	;;
esac
