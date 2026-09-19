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

dump() {
  class=$1 container=$2 database=$3
  dir=$(dir_of "$class" "$container")
  umask 077
  mkdir -p "$dir"

  last=$(ls -1 "$dir" 2>/dev/null | grep '\.dump$' | sort | tail -n 1 || true)
  if [ -n "$last" ]; then
    size=$(wc -c <"$dir/$last")
    free=$(df -B1 --output=avail "$dir" | tail -n 1 | tr -d ' ')
    if [ "$free" -lt $((size * 2)) ]; then
      abort "$dir has $free bytes free and the last dump of $container took $size: a dump is taken with room for two, because a full disk takes the database down with it"
    fi
  fi

  stamp=$(date -u +%Y%m%dT%H%M%SZ)
  partial="$dir/.$stamp.$$.partial"
  if ! docker exec "$container" pg_dump -U "$superuser" -Fc -d "$database" >"$partial"; then
    rm -f "$partial"
    abort "pg_dump of $database in $container failed, and nothing was kept of it"
  fi
  roles="$dir/.$stamp.$$.roles.partial"
  if ! docker exec "$container" pg_dumpall -U "$superuser" --roles-only >"$roles"; then
    rm -f "$partial" "$roles"
    abort "pg_dumpall of the roles in $container failed, and nothing was kept of it"
  fi
  mv -f "$roles" "$dir/$stamp.roles.sql"
  mv -f "$partial" "$dir/$stamp.dump"

  ls -1 "$dir" | grep '\.dump$' | sort -r | tail -n +$((keep + 1)) | while read -r old; do
    rm -f "$dir/$old" "$dir/${old%.dump}.roles.sql"
  done
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
  docker ps --filter label=ocel.backup=pg \
    --format '{{.Names}}	{{.Label "ocel.class"}}	{{.Label "ocel.resource"}}' |
    while IFS='	' read -r container class database; do
      [ -n "$container" ] || continue
      ( dump "$class" "$container" "$database" >/dev/null ) || echo failed
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
