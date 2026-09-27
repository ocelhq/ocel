#!/bin/sh
set -u

docker container prune --force
docker image prune --all --force
docker builder prune --all --force
docker system prune --all --force
