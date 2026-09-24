#!/bin/sh
set -u
image=$(docker image inspect ocel-proto/app:v1 --format '{{index .RepoDigests 0}}')
body=$(printf '{"Name":"nnp-probe","TaskTemplate":{"ContainerSpec":{"Image":"%s","Env":["VERSION=nnp"],"Privileges":{"NoNewPrivileges":true},"CapabilityDrop":["ALL"],"CapabilityAdd":["CAP_NET_BIND_SERVICE"]},"Resources":{"Limits":{"Pids":4096}}},"Mode":{"Replicated":{"Replicas":1}}}' "$image")
curl -s --unix-socket /var/run/docker.sock -H 'Content-Type: application/json' -d "$body" http://localhost/v1.52/services/create
echo
sleep 12
ctr=$(docker ps -q -f label=com.docker.swarm.service.name=nnp-probe | head -1)
docker inspect "$ctr" --format 'SecurityOpt={{.HostConfig.SecurityOpt}} CapDrop={{.HostConfig.CapDrop}} CapAdd={{.HostConfig.CapAdd}} PidsLimit={{.HostConfig.PidsLimit}}'
docker service rm nnp-probe >/dev/null
