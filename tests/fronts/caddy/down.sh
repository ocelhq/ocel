#!/bin/sh
set -eu

export DEBIAN_FRONTEND=noninteractive
systemctl disable --now caddy >/dev/null 2>&1 || true
if dpkg-query -W caddy >/dev/null 2>&1; then
    apt-get -o DPkg::Lock::Timeout=300 purge -y -qq caddy >/dev/null
fi
rm -rf /etc/caddy /var/lib/caddy /var/lib/ocel-front/caddy
