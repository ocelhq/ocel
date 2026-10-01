#!/bin/sh
set -eu

state=/var/lib/ocel-front/caddy

if ! command -v caddy >/dev/null 2>&1; then
    export DEBIAN_FRONTEND=noninteractive
    apt-get update -qq
    apt-get -o DPkg::Lock::Timeout=300 install -y -qq caddy >/dev/null
fi

install -d -m 0755 /etc/caddy/ocel.d
cat > /etc/caddy/Caddyfile <<'EOF'
{
	local_certs
	skip_install_trust
}

import /etc/caddy/ocel.d/*.caddy

mine.localhost {
	respond "mine"
}
EOF

systemctl enable --now caddy >/dev/null 2>&1
systemctl reload caddy

install -d -m 0755 "$state"
sha256sum /etc/caddy/Caddyfile > "$state/config.sum"
