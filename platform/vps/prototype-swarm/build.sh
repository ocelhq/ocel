#!/usr/bin/env bash
set -euo pipefail
here="$(cd "$(dirname "$0")" && pwd)"
lab="${LAB:?set LAB to a scratch directory}"
caddy=public.ecr.aws/docker/library/caddy@sha256:df7f1c2fb114453b951de51a98efc010db1655a92c2e86be6706714e2417a78d

cd "$here"
export GOWORK=off
CGO_ENABLED=0 go build -o app/app ./app
go build -o "$lab/loadgen" ./loadgen
for v in v1 v2 v3; do
    docker build -q -t "ocel-proto/app:$v" --label "version=$v" app >/dev/null
done
docker pull -q "$caddy" >/dev/null
docker tag "$caddy" ocel-proto/caddy:pinned
docker save ocel-proto/app:v1 ocel-proto/app:v2 ocel-proto/app:v3 > "$lab/app.tar"
docker save ocel-proto/caddy:pinned > "$lab/caddy.tar"
ls -la "$lab"/*.tar
