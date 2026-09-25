#!/bin/sh
set -eu

export DEBIAN_FRONTEND=noninteractive
systemctl disable --now nginx >/dev/null 2>&1 || true
apt-get purge -y -qq nginx nginx-common nginx-core >/dev/null 2>&1 || true
rm -rf /etc/nginx /var/lib/ocel-front/nginx
