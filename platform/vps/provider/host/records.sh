#!/bin/sh
set -eu
umask 077

usage() {
	echo "usage: records <class> read|write|pair|remove|list [args]" >&2
	exit 2
}

abort() {
	echo "records: $1" >&2
	exit 2
}

[ $# -ge 2 ] || usage
class=$1
verb=$2
shift 2

case $class in
'' | *[!a-z0-9-]*) abort "$class is no class this host keeps records for" ;;
esac

dir="${OCEL_RECORDS_ROOT:-/var/lib/ocel}/$class/records"
[ -d "$dir" ] || abort "$dir stands as no record tier, and ocel bootstrap is what writes one"

belongs() {
	[ "$(id -u)" = 0 ] || return 0
	p=$1
	while [ "$p" != "$dir" ]; do
		chown --reference="$dir" "$p" ||
			abort "$p was written as root and could not be handed to whoever owns $dir, which is the login every deploy after this one reads it as"
		p=$(dirname "$p")
	done
}

lock="$dir/.lock"
: >>"$lock"
belongs "$lock"
exec 9>"$lock"
flock -x 9

fileof() { printf '%s/%s.rec' "$dir" "$1"; }

readrev() {
	held=
	[ -f "$1" ] || return 0
	held=$(head -n1 "$1")
	[ -n "$held" ] || abort "$1 names no revision, and ocel overwrites nothing it cannot compare"
}

mint() {
	rev=$(od -An -N16 -tx1 /dev/urandom | tr -d ' \n')
	[ ${#rev} -eq 32 ] || abort "this host minted no revision, and a record nothing can compare is a record anything can lose"
	case $rev in
	*[!0-9a-f]*) abort "this host minted no revision, and a record nothing can compare is a record anything can lose" ;;
	esac
}

checked() {
	case $1 in
	*[!A-Za-z0-9+/=]*) abort "a record body arrived as bytes ocel never encodes" ;;
	esac
}

stage() {
	staged="$1.staged"
	mkdir -p "$(dirname "$1")"
	printf '%s\n%s\n' "$2" "$3" >"$staged"
}

prune() {
	d=$(dirname "$1")
	while [ "$d" != "$dir" ]; do
		rmdir "$d" 2>/dev/null || break
		d=$(dirname "$d")
	done
}

emit() {
	printf '%s\t%s\t%s\n' "$1" "$(head -n1 "$2")" "$(sed -n 2p "$2")"
}

case "$verb" in
read)
	[ $# -eq 1 ] || usage
	f=$(fileof "$1")
	[ -f "$f" ] || exit 3
	printf '%s\t%s\n' "$(head -n1 "$f")" "$(sed -n 2p "$f")"
	;;
write)
	[ $# -eq 2 ] || usage
	f=$(fileof "$1")
	readrev "$f"
	[ "$held" = "$2" ] || exit 4
	body=$(cat)
	checked "$body"
	mint
	stage "$f" "$rev" "$body"
	mv -f "$staged" "$f"
	belongs "$f"
	printf '%s\n' "$rev"
	;;
pair)
	[ $# -eq 4 ] || usage
	first=$(fileof "$1")
	second=$(fileof "$3")
	readrev "$first"
	[ "$held" = "$2" ] || exit 4
	readrev "$second"
	[ "$held" = "$4" ] || exit 4
	IFS= read -r one || abort "a pair arrived carrying one body, and ocel writes no half it was never given"
	IFS= read -r two || abort "a pair arrived carrying one body, and ocel writes no half it was never given"
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
	belongs "$first"
	belongs "$second"
	printf '%s\t%s\n' "$onerev" "$tworev"
	;;
remove)
	[ $# -eq 2 ] || usage
	f=$(fileof "$1")
	[ -f "$f" ] || exit 3
	readrev "$f"
	[ "$held" = "$2" ] || exit 4
	rm -f "$f"
	prune "$f"
	echo removed
	;;
list)
	[ $# -le 1 ] || usage
	under=$dir
	if [ $# -eq 1 ] && [ -n "$1" ]; then
		under="$dir/$1"
	fi
	[ -d "$under" ] || exit 0
	found="$dir/.list"
	find "$under" -type f -name '*.rec' >"$found"
	belongs "$found"
	while IFS= read -r f; do
		name=${f#"$dir/"}
		emit "${name%.rec}" "$f"
	done <"$found"
	rm -f "$found"
	;;
*)
	usage
	;;
esac
