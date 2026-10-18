#!/bin/sh
set -eu

root=${OCEL_BACKUPS_ROOT:-/var/lib/ocel}
keep=7
superuser=postgres
copier=@STORE_COPIER@
stop_seconds=60

kv_snapshot=dump.rdb
kv_aof_dir=appendonlydir
kv_aof_base=appendonly.aof.1.base.rdb
kv_aof_manifest=appendonly.aof.manifest

mirror=current

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

stamped() {
  date -u +%Y%m%dT%H%M%SZ
}

label_of() {
  docker inspect --type container --format "{{index .Config.Labels \"$2\"}}" "$1"
}

env_of() {
  docker inspect --type container --format '{{range .Config.Env}}{{println .}}{{end}}' "$1" | sed -n "s/^$2=//p" | head -n 1
}

volume_of() {
  source=$(docker inspect --type container --format '{{range .Mounts}}{{if eq .Type "volume"}}{{.Source}}{{end}}{{end}}' "$1")
  [ -n "$source" ] || abort "$1 mounts no volume"
  printf '%s' "$source"
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
    rm -rf "${dir:?}/$old" "$dir/${old%$suffix}.roles.sql"
  done
}

dump_pg() {
  tier=$1 container=$2
  database=$(label_of "$container" ocel.resource)
  [ -n "$database" ] || abort "$container names no database in its ocel.resource label"
  dir=$(dir_of "$tier" "$container")
  umask 077
  mkdir -p "$dir"
  room_for "$dir" "$container" .dump

  stamp=$(stamped)
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
  owned "$tier" "$root/$tier/backups"
  printf '%s\n' "$dir/$stamp.dump"
}

dump_kv() {
  tier=$1 container=$2
  dir=$(dir_of "$tier" "$container")
  umask 077
  mkdir -p "$dir"
  room_for "$dir" "$container" .rdb

  source=$(volume_of "$container")
  [ -s "$source/$kv_snapshot" ] || abort "$container has saved no snapshot to $source/$kv_snapshot yet"
  stamp=$(stamped)
  partial="$dir/.$stamp.$$.partial"
  if ! cp "$source/$kv_snapshot" "$partial"; then
    rm -f "$partial"
    abort "copying the snapshot $container saved at $source/$kv_snapshot failed"
  fi
  mv -f "$partial" "$dir/$stamp.rdb"
  keep_newest "$dir" .rdb
  owned "$tier" "$root/$tier/backups"
  printf '%s\n' "$dir/$stamp.rdb"
}

copy_store() {
  container=$1 mounted=$2 mode=$3
  shift 3
  network=$(docker inspect --type container --format '{{range $name, $_ := .NetworkSettings.Networks}}{{println $name}}{{end}}' "$container" | head -n 1)
  [ -n "$network" ] || abort "$container is on no network a copy could reach it over"
  access=$(env_of "$container" RUSTFS_ACCESS_KEY)
  secret=$(env_of "$container" RUSTFS_SECRET_KEY)
  port=$(env_of "$container" RUSTFS_ADDRESS)
  [ -n "$access" ] && [ -n "$secret" ] || abort "$container carries no credential a copy could sign with"
  env=$(mktemp)
  trap 'rm -f "$env"' EXIT INT TERM
  chmod 600 "$env"
  printf '%s\n' \
    "RCLONE_CONFIG_STORE_TYPE=s3" \
    "RCLONE_CONFIG_STORE_PROVIDER=Other" \
    "RCLONE_CONFIG_STORE_ENDPOINT=http://$container${port:-:9000}" \
    "RCLONE_CONFIG_STORE_ACCESS_KEY_ID=$access" \
    "RCLONE_CONFIG_STORE_SECRET_ACCESS_KEY=$secret" \
    "RCLONE_CONFIG_STORE_FORCE_PATH_STYLE=true" >"$env"
  status=0
  docker run --rm --network "$network" --env-file "$env" --user "$(stat -c %u:%g "$mounted")" \
    --cap-drop ALL --security-opt no-new-privileges \
    --mount "type=bind,src=$mounted,dst=/backup$mode" \
    "$copier" "$@" </dev/null || status=$?
  rm -f "$env"
  trap - EXIT INT TERM
  return "$status"
}

room_for_store() {
  dir=$1 container=$2
  source=$(volume_of "$container")
  stored=$(du -sb "$source" | cut -f1)
  copied=$(du -sb "$dir/$mirror" | cut -f1)
  free=$(df -B1 --output=avail "$dir" | tail -n 1 | tr -d ' ')
  if [ "$free" -lt $((stored - copied + stored / 10)) ]; then
    abort "$dir has $free bytes free; $container stores $stored, $copied of them already copied, and a copy needs room for the rest and a tenth more"
  fi
}

dump_store() {
  tier=$1 container=$2
  dir=$(dir_of "$tier" "$container")
  umask 077
  mkdir -p "$dir/$mirror"
  room_for_store "$dir" "$container"
  stamp=$(stamped)
  copy_store "$container" "$dir" "" sync store: "/backup/$mirror" --backup-dir "/backup/$stamp" ||
    abort "copying the objects $container stores failed"
  ls -1 "$dir" | grep -v "^$mirror\$" | sort -r | tail -n +$((keep + 1)) | while read -r old; do
    rm -rf "${dir:?}/$old"
  done
  owned "$tier" "$root/$tier/backups"
  printf '%s\n' "$dir/$mirror"
}

dump() {
  tier=$1 container=$2
  kind=$(label_of "$container" ocel.backup) || abort "$container is not a container on this host"
  case "$kind" in
  pg) dump_pg "$tier" "$container" ;;
  kv) dump_kv "$tier" "$container" ;;
  s3) dump_store "$tier" "$container" ;;
  *) abort "$container asks for no backup" ;;
  esac
}

restore_pg() {
  container=$1 file=$2
  database=$(label_of "$container" ocel.resource)
  [ -n "$database" ] || abort "$container names no database in its ocel.resource label"
  roles="${file%.dump}.roles.sql"
  if [ -s "$roles" ]; then
    docker exec --interactive "$container" psql -U "$superuser" -d "$database" <"$roles" >/dev/null 2>&1 || true
  fi
  owner=$(label_of "$container" ocel.database-owner)
  docker exec --interactive "$container" pg_restore -U "$superuser" ${owner:+--role="$owner"} --clean --if-exists --no-owner -d "$database" <"$file" ||
    abort "pg_restore of $file into $database in $container failed"
}

restore_kv() {
  container=$1 file=$2
  source=$(volume_of "$container")
  aside="$source/.before-restore-$(stamped)"
  docker stop --time "$stop_seconds" "$container" >/dev/null || abort "$container would not stop to take the snapshot back"
  writing=0
  put_back() {
    for held in "$kv_aof_dir" "$kv_snapshot"; do
      if [ "$writing" -eq 1 ]; then rm -rf "${source:?}/$held"; fi
      if [ -e "$aside/$held" ]; then mv "$aside/$held" "$source/$held"; fi
    done
    rmdir "$aside" 2>/dev/null || true
    docker start "$container" >/dev/null 2>&1 || true
  }
  trap put_back EXIT INT TERM
  mkdir "$aside"
  for held in "$kv_aof_dir" "$kv_snapshot"; do
    if [ -e "$source/$held" ]; then mv "$source/$held" "$aside/$held"; fi
  done
  writing=1
  mkdir "$source/$kv_aof_dir"
  cp "$file" "$source/$kv_aof_dir/$kv_aof_base"
  printf 'file %s seq 1 type b\n' "$kv_aof_base" >"$source/$kv_aof_dir/$kv_aof_manifest"
  cp "$file" "$source/$kv_snapshot"
  chown -R --reference="$source" "$source/$kv_aof_dir" "$source/$kv_snapshot"
  docker start "$container" >/dev/null
  trap - EXIT INT TERM
  printf 'what %s held before the restore is at %s\n' "$container" "$aside"
}

restore_store() {
  container=$1 file=$2
  [ -d "$file" ] || abort "$file is not a copy of a store this host keeps"
  copy_store "$container" "$file" ",readonly" sync /backup store: ||
    abort "copying $file back into $container failed"
}

restore() {
  tier=$1 container=$2 file=${3:-}
  kind=$(label_of "$container" ocel.backup) || abort "$container is not a container on this host"
  if [ -z "$file" ]; then
    dir=$(dir_of "$tier" "$container")
    case "$kind" in
    pg) file=$(ls -1 "$dir" 2>/dev/null | grep '\.dump$' | sort | tail -n 1 || true) ;;
    kv) file=$(ls -1 "$dir" 2>/dev/null | grep '\.rdb$' | sort | tail -n 1 || true) ;;
    s3) file=$mirror ;;
    esac
    [ -n "$file" ] || abort "$dir holds no backup of $container"
    file="$dir/$file"
  fi
  [ -e "$file" ] && [ -s "$file" ] || [ -d "$file" ] || abort "$file is not a backup this host keeps"
  case "$kind" in
  pg) restore_pg "$container" "$file" ;;
  kv) restore_kv "$container" "$file" ;;
  s3) restore_store "$container" "$file" ;;
  *) abort "$container asks for no backup, so none restores into it" ;;
  esac
}

sweep() {
  failed=0
  asking=$(docker ps --filter label=ocel.backup --format '{{.Names}}	{{.Label "ocel.tier"}}') ||
    abort "docker could not list the containers that ask for a backup"
  while IFS='	' read -r container tier; do
    [ -n "$container" ] || continue
    if ! sh "$0" "$tier" dump "$container" >/dev/null </dev/null; then
      printf 'the backup of %s failed\n' "$container" >&2
      failed=1
    fi
  done <<EOF
$asking
EOF
  return $failed
}

usage="usage: backups sweep | backups <tier> dump <container> | backups <tier> restore <container> [<backup>]"

case "${1:-}" in
sweep)
  sweep
  ;;
*)
  [ $# -ge 3 ] || abort "$usage"
  tier=$1 verb=$2
  shift 2
  case "$verb" in
  dump)
    [ $# -eq 1 ] || abort "$usage"
    dump "$tier" "$1"
    ;;
  restore)
    [ $# -le 2 ] || abort "$usage"
    restore "$tier" "$@"
    ;;
  *)
    abort "backups knows dump, restore and sweep, not $verb"
    ;;
  esac
  ;;
esac
