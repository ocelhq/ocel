#!/bin/sh
set -eu

export DEBIAN_FRONTEND=noninteractive
systemctl disable --now nginx >/dev/null 2>&1 || true
installed=$(dpkg-query -W -f '${Package}\n' nginx nginx-common nginx-core 2>/dev/null || true)
if [ -n "$installed" ]; then
    apt-get -o DPkg::Lock::Timeout=300 purge -y -qq $installed >/dev/null
fi
rm -rf /etc/nginx /var/lib/ocel-front/nginx
