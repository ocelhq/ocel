#!/bin/sh
set -eu

root=${OCEL_BACKUPS_ROOT:-/var/lib/ocel}
keep=7
superuser=postgres

abort() {
  printf '%s\n' "$1" >&2
  exit 1
}

dir_of() {
  printf '%s/%s/backups/%s' "$root" "$1" "$2"
}

owned() {
  if [ "$(id -u)" -eq 0 ]; then
    chown -R --reference="$root/$1" "$2"
  fi
}

room_for() {
  dir=$1 container=$2 suffix=$3
  last=$(ls -1 "$dir" 2>/dev/null | grep "$suffix\$" | sort | tail -n 1 || true)
  [ -n "$last" ] || return 0
  size=$(wc -c <"$dir/$last")
  free=$(df -B1 --output=avail "$dir" | tail -n 1 | tr -d ' ')
  if [ "$free" -lt $((size * 2)) ]; then
    abort "$dir has $free bytes free; the last dump of $container took $size and a dump needs twice that"
  fi
}

keep_newest() {
  dir=$1 suffix=$2
  ls -1 "$dir" | grep "$suffix\$" | sort -r | tail -n +$((keep + 1)) | while read -r old; do
    rm -f "$dir/$old" "$dir/${old%$suffix}.roles.sql"
  done
}

dump_volume() {
  class=$1 container=$2
  dir=$(dir_of "$class" "$container")
  umask 077
  mkdir -p "$dir"
  room_for "$dir" "$container" .tar

  source=$(docker inspect --format '{{range .Mounts}}{{if eq .Type "volume"}}{{.Source}}{{end}}{{end}}' "$container")
  [ -n "$source" ] || abort "$container mounts no volume"

  stamp=$(date -u +%Y%m%dT%H%M%SZ)
  partial="$dir/.$stamp.$$.partial"
  docker pause "$container" >/dev/null ||
    abort "$container would not pause"
  trap 'docker unpause "$container" >/dev/null 2>&1 || true' EXIT INT TERM
  if ! tar -C "$source" -cf "$partial" .; then
    rm -f "$partial"
    abort "tar of $source for $container failed"
  fi
  docker unpause "$container" >/dev/null 2>&1 || true
  trap - EXIT INT TERM
  mv -f "$partial" "$dir/$stamp.tar"
  keep_newest "$dir" .tar
  owned "$class" "$root/$class/backups"
  printf '%s\n' "$dir/$stamp.tar"
}

dump() {
  class=$1 container=$2 database=$3
  dir=$(dir_of "$class" "$container")
  umask 077
  mkdir -p "$dir"
  room_for "$dir" "$container" .dump

  stamp=$(date -u +%Y%m%dT%H%M%SZ)
  partial="$dir/.$stamp.$$.partial"
  if ! docker exec "$container" pg_dump -U "$superuser" -Fc -d "$database" >"$partial"; then
    rm -f "$partial"
    abort "pg_dump of $database in $container failed"
  fi
  roles="$dir/.$stamp.$$.roles.partial"
  if ! docker exec "$container" pg_dumpall -U "$superuser" --roles-only >"$roles"; then
    rm -f "$partial" "$roles"
    abort "pg_dumpall of the roles in $container failed"
  fi
  mv -f "$roles" "$dir/$stamp.roles.sql"
  mv -f "$partial" "$dir/$stamp.dump"

  keep_newest "$dir" .dump
  owned "$class" "$root/$class/backups"
  printf '%s\n' "$dir/$stamp.dump"
}

restore() {
  container=$1 database=$2 file=$3
  [ -s "$file" ] || abort "$file is not a dump this host holds"
  roles="${file%.dump}.roles.sql"
  if [ -s "$roles" ]; then
    docker exec --interactive "$container" psql -U "$superuser" -d "$database" <"$roles" >/dev/null 2>&1 || true
  fi
  docker exec --interactive "$container" pg_restore -U "$superuser" --clean --if-exists --no-owner -d "$database" <"$file" ||
    abort "pg_restore of $file into $database in $container failed"
}

sweep() {
  failed=0
  docker ps --filter label=ocel.backup \
    --format '{{.Names}}	{{.Label "ocel.class"}}	{{.Label "ocel.resource"}}	{{.Label "ocel.backup"}}' |
    while IFS='	' read -r container class database kind; do
      [ -n "$container" ] || continue
      case "$kind" in
      pg) ( dump "$class" "$container" "$database" >/dev/null ) || echo failed ;;
      vol) ( dump_volume "$class" "$container" >/dev/null ) || echo failed ;;
      *) ;;
      esac
    done | grep -q failed && failed=1
  return $failed
}

case "${1:-}" in
sweep)
  sweep
  ;;
*)
  [ $# -ge 2 ] || abort "usage: backups sweep | backups <class> dump <container> <database> | backups <class> restore <container> <database> <file>"
  class=$1 verb=$2
  shift 2
  case "$verb" in
  dump)
    [ $# -eq 2 ] || abort "usage: backups <class> dump <container> <database>"
    dump "$class" "$1" "$2"
    ;;
  restore)
    [ $# -eq 3 ] || abort "usage: backups <class> restore <container> <database> <file>"
    restore "$1" "$2" "$3"
    ;;
  *)
    abort "backups knows dump, restore and sweep, not $verb"
    ;;
  esac
  ;;
esac
