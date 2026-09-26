#!/bin/sh
set -eu

image=public.ecr.aws/nginx/nginx:stable-alpine
conf=/etc/ocel-front-container
state=/var/lib/ocel-front/nginx-container
[ "$#" -gt 0 ] || {
    echo "up.sh: name the hostnames the certificate covers, wildcards allowed" >&2
    exit 2
}

if ! command -v docker >/dev/null 2>&1; then
    curl -fsSL --retry 5 --retry-delay 2 https://get.docker.com | sh >/dev/null
fi
if ! command -v openssl >/dev/null 2>&1; then
    export DEBIAN_FRONTEND=noninteractive
    apt-get update -qq
    apt-get install -y -qq openssl >/dev/null
fi
docker network inspect ocel >/dev/null 2>&1 || docker network create ocel >/dev/null

names=""
for name in "$@"; do
    names="${names:+$names,}DNS:$name"
done
install -d -m 0755 "$conf"
openssl req -x509 -newkey rsa:2048 -nodes -days 30 -subj /CN=ocel-front \
    -addext "subjectAltName=$names" \
    -keyout "$conf/key.pem" -out "$conf/cert.pem" 2>/dev/null
chmod 0644 "$conf/key.pem"

cat > "$conf/default.conf" <<'EOF'
map $http_upgrade $ocel_connection {
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
    resolver 127.0.0.11 valid=10s ipv6=off;

    location / {
        set $switchboard http://ocel-switchboard:8080;
        proxy_pass $switchboard;
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection $ocel_connection;
        proxy_buffering off;
        proxy_read_timeout 3600s;
    }
}
EOF

cat > "$conf/compose.yaml" <<EOF
services:
  front:
    image: $image
    container_name: ocel-front-nginx
    restart: unless-stopped
    ports:
      - "80:80"
      - "443:443"
    volumes:
      - $conf/default.conf:/etc/nginx/conf.d/default.conf:ro
      - $conf/cert.pem:/etc/nginx/ocel-front/cert.pem:ro
      - $conf/key.pem:/etc/nginx/ocel-front/key.pem:ro
    networks:
      - ocel

networks:
  ocel:
    external: true
EOF

docker compose -f "$conf/compose.yaml" up -d

install -d -m 0755 "$state"
find "$conf" -type f | LC_ALL=C sort | xargs sha256sum > "$state/config.sum"
