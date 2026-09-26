#!/bin/sh
set -eu

series=${1:-}
case "$series" in
[0-9]*.[0-9]*) ;;
*)
    echo "usage: sudo sh scripts/vps-docker.sh <major.minor>, installs the newest docker of that series from docker's apt repository" >&2
    exit 2
    ;;
esac

export DEBIAN_FRONTEND=noninteractive
. /etc/os-release
apt-get update -qq
apt-get install -y -qq ca-certificates curl >/dev/null
install -m 0755 -d /etc/apt/keyrings
curl -fsSL --retry 5 --retry-delay 2 "https://download.docker.com/linux/$ID/gpg" -o /etc/apt/keyrings/docker.asc
chmod a+r /etc/apt/keyrings/docker.asc
printf 'deb [arch=%s signed-by=/etc/apt/keyrings/docker.asc] https://download.docker.com/linux/%s %s stable\n' \
    "$(dpkg --print-architecture)" "$ID" "$VERSION_CODENAME" >/etc/apt/sources.list.d/docker.list
apt-get update -qq

version=$(apt-cache madison docker-ce | awk -v series="5:$series." 'index($3, series) == 1 { print $3; exit }')
[ -n "$version" ] || {
    echo "vps-docker.sh: docker's apt repository has no docker-ce $series.x for $ID $VERSION_CODENAME" >&2
    exit 1
}
apt-get install -y -qq "docker-ce=$version" "docker-ce-cli=$version" containerd.io >/dev/null
systemctl enable --now docker.service >/dev/null
docker version --format '{{.Server.Version}}'
