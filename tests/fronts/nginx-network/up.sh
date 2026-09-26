#!/bin/sh
set -eu

image=public.ecr.aws/nginx/nginx:stable-alpine
network=ocel-front
conf=/etc/ocel-front-network
state=/var/lib/ocel-front/nginx-network
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
docker network inspect "$network" >/dev/null 2>&1 || docker network create "$network" >/dev/null

install -d -m 0755 "$conf"
if [ ! -f "$conf/cert.pem" ]; then
    names=""
    for name in "$@"; do
        names="${names:+$names,}DNS:$name"
    done
    openssl req -x509 -newkey rsa:2048 -nodes -days 30 -subj /CN=ocel-front \
        -addext "subjectAltName=$names" \
        -keyout "$conf/key.pem" -out "$conf/cert.pem" 2>/dev/null
    chmod 0644 "$conf/key.pem"
fi

cat > "$conf/default.conf" <<'CONF'
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
CONF

cat > "$conf/compose.yaml" <<COMPOSE
services:
  front:
    image: $image
    container_name: ocel-front-nginx-network
    restart: unless-stopped
    ports:
      - "80:80"
      - "443:443"
    volumes:
      - $conf/default.conf:/etc/nginx/conf.d/default.conf:ro
      - $conf/cert.pem:/etc/nginx/ocel-front/cert.pem:ro
      - $conf/key.pem:/etc/nginx/ocel-front/key.pem:ro
    networks:
      - $network

networks:
  $network:
    external: true
COMPOSE

docker compose -f "$conf/compose.yaml" up -d --force-recreate

install -d -m 0755 "$state"
find "$conf" -type f | LC_ALL=C sort | xargs sha256sum > "$state/config.sum"
