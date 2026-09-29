#!/bin/sh
set -eu

traefik=mirror.gcr.io/library/traefik:v3.6
pebble=ghcr.io/letsencrypt/pebble:latest
mine=mirror.gcr.io/library/nginx:stable-alpine
conf=/etc/ocel-front-traefik
state=/var/lib/ocel-front/traefik

if ! command -v docker >/dev/null 2>&1; then
    curl -fsSL --retry 5 --retry-delay 2 https://get.docker.com | sh >/dev/null
fi

install -d -m 0755 "$conf" "$conf/dynamic" "$state"
install -d -m 0700 "$conf/acme"

docker rm -f ocel-front-pebble >/dev/null 2>&1 || true
docker run --detach --name ocel-front-pebble --restart unless-stopped \
    --publish 127.0.0.1:14000:14000 \
    --env PEBBLE_VA_ALWAYS_VALID=1 --env PEBBLE_VA_NOSLEEP=1 \
    "$pebble" >/dev/null
docker cp ocel-front-pebble:/test/certs/pebble.minica.pem "$conf/pebble.minica.pem"

docker rm -f ocel-front-mine >/dev/null 2>&1 || true
docker run --detach --name ocel-front-mine --restart unless-stopped \
    --publish 127.0.0.1:8081:80 "$mine" >/dev/null

cat > "$conf/traefik.yml" <<YML
entryPoints:
  web:
    address: ":80"
    http:
      encodeQuerySemicolons: true
  websecure:
    address: ":443"
    http:
      encodeQuerySemicolons: true
providers:
  file:
    directory: $conf/dynamic
    watch: true
certificatesResolvers:
  pebble:
    acme:
      email: ops@p1306.test
      caServer: https://localhost:14000/dir
      caCertificates:
        - $conf/pebble.minica.pem
      storage: $conf/acme/acme.json
      httpChallenge:
        entryPoint: web
log:
  level: WARN
YML

cat > "$conf/dynamic/mine.yml" <<'YML'
http:
  routers:
    mine:
      rule: Host(`mine.p1306.test`)
      entryPoints: [websecure]
      service: mine
      tls:
        certResolver: pebble
  services:
    mine:
      loadBalancer:
        servers:
          - url: http://127.0.0.1:8081
YML

docker rm -f ocel-front-traefik >/dev/null 2>&1 || true
docker run --detach --name ocel-front-traefik --restart unless-stopped \
    --network host \
    --volume "$conf:$conf" \
    "$traefik" --configFile="$conf/traefik.yml" >/dev/null

find "$conf" -type f ! -path "$conf/acme/*" | LC_ALL=C sort | xargs sha256sum > "$state/config.sum"
