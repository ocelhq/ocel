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
'' | *[!a-z0-9-]*) abort "$class is not a valid class" ;;
esac

dir="${OCEL_RECORDS_ROOT:-/var/lib/ocel}/$class/records"
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

fileof() { printf '%s/%s.rec' "$dir" "$1"; }

readrev() {
	current=
	[ -f "$1" ] || return 0
	current=$(head -n1 "$1")
	[ -n "$current" ] || abort "$1 names no revision"
}

mint() {
	rev=$(od -An -N16 -tx1 /dev/urandom | tr -d ' \n')
	[ ${#rev} -eq 32 ] || abort "could not mint a revision"
	case $rev in
	*[!0-9a-f]*) abort "could not mint a revision" ;;
	esac
}

checked() {
	case $1 in
	*[!A-Za-z0-9+/=]*) abort "a record body is not base64" ;;
	esac
}

stage() {
	staged="$1.staged"
	mkdir -p "$(dirname "$1")"
	ours "$staged"
	printf '%s\n%s\n' "$2" "$3" >"$staged"
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
	printf '%s\t%s\t%s\n' "$1" "$(head -n1 "$2")" "$(sed -n 2p "$2")"
}

case "$verb" in
read)
	[ $# -eq 1 ] || usage
	f=$(fileof "$1")
	ours "$f"
	[ -f "$f" ] || exit 3
	printf '%s\t%s\n' "$(head -n1 "$f")" "$(sed -n 2p "$f")"
	;;
write)
	[ $# -eq 2 ] || usage
	f=$(fileof "$1")
	ours "$f"
	readrev "$f"
	[ "$current" = "$2" ] || exit 4
	body=$(cat)
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
	[ $# -le 1 ] || usage
	under=$dir
	if [ $# -eq 1 ] && [ -n "$1" ]; then
		under="$dir/$1"
		ours "$under"
	fi
	[ -d "$under" ] || exit 0
	found="$dir/.list"
	ours "$found"
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
