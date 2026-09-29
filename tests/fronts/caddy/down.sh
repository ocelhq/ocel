#!/bin/sh
set -eu

export DEBIAN_FRONTEND=noninteractive
systemctl disable --now caddy >/dev/null 2>&1 || true
apt-get purge -y -qq caddy >/dev/null 2>&1 || true
rm -rf /etc/caddy /var/lib/caddy /var/lib/ocel-front/caddy
