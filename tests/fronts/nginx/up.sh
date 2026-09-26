#!/bin/sh
set -eu

port=${OCEL_FRONT_PORT:-8480}
state=/var/lib/ocel-front/nginx
[ "$#" -gt 0 ] || {
    echo "up.sh: name the hostnames the certificate covers, wildcards allowed" >&2
    exit 2
}

if ! command -v nginx >/dev/null 2>&1 || ! command -v openssl >/dev/null 2>&1; then
    export DEBIAN_FRONTEND=noninteractive
    apt-get update -qq
    apt-get install -y -qq nginx openssl >/dev/null
fi

names=""
for name in "$@"; do
    names="${names:+$names,}DNS:$name"
done
install -d -m 0755 /etc/nginx/ocel-front
openssl req -x509 -newkey rsa:2048 -nodes -days 30 -subj /CN=ocel-front \
    -addext "subjectAltName=$names" \
    -keyout /etc/nginx/ocel-front/key.pem -out /etc/nginx/ocel-front/cert.pem 2>/dev/null

rm -f /etc/nginx/sites-enabled/default
cat > /etc/nginx/conf.d/ocel-front.conf <<EOF
map \$http_upgrade \$ocel_connection {
    default upgrade;
    '' close;
}

server {
    listen 80 default_server;
    listen 443 ssl default_server;
    server_name _;
    client_max_body_size 0;
    ssl_certificate /etc/nginx/ocel-front/cert.pem;
    ssl_certificate_key /etc/nginx/ocel-front/key.pem;

    location / {
        proxy_pass http://127.0.0.1:$port;
        proxy_http_version 1.1;
        proxy_set_header Host \$host;
        proxy_set_header X-Forwarded-Proto \$scheme;
        proxy_set_header X-Forwarded-For \$proxy_add_x_forwarded_for;
        proxy_set_header Upgrade \$http_upgrade;
        proxy_set_header Connection \$ocel_connection;
        proxy_buffering off;
        proxy_read_timeout 3600s;
    }
}
EOF

nginx -t 2>/dev/null
systemctl enable --now nginx >/dev/null 2>&1
systemctl reload nginx

install -d -m 0755 "$state"
find /etc/nginx -type f | LC_ALL=C sort | xargs sha256sum > "$state/config.sum"
