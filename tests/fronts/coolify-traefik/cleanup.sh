#!/bin/sh
set -u

docker container prune -f --filter "label=coolify.managed=true" --filter "label!=coolify.proxy=true" --filter "label!=coolify.type=database" --filter "label!=coolify.type=application" --filter "label!=coolify.type=service"
docker image prune -f
docker images --format '{{.Repository}}:{{.Tag}}' | grep -v '<none>' |
    xargs -r -I {} sh -c 'docker inspect --format "{{index .Config.Labels \"coolify.managed\"}}" "{}" 2>/dev/null | grep -q true || docker rmi "{}" 2>/dev/null' || true
docker builder prune -af
docker volume prune -af
docker network prune -f
