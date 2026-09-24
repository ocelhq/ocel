#!/usr/bin/env bash
set -uo pipefail
here="$(cd "$(dirname "$0")" && pwd)"
lab="${LAB:?set LAB to the scratch directory build.sh wrote to}"
domain=demo.test

backend=${BACKEND:-vm}
labvm=${LABVM:-swarm-2}

vmaddr() { incus list "^$1\$" -c4 -f csv | tr -d '"' | tr ',' '\n' | grep enp5s0 | awk '{print $1; exit}'; }
on() {
    local n=$1; shift
    if [ "$backend" = dind ]; then
        incus exec "$labvm" -- docker exec "n$n" sh -c "$*"
    else
        incus exec "swarm-$n" -- sh -c "$*"
    fi
}
addr() { if [ "$backend" = dind ]; then echo "172.30.0.1$1"; else vmaddr "swarm-$1"; fi; }
entry() { if [ "$backend" = dind ]; then vmaddr "$labvm"; else addr 1; fi; }
push() {
    local src=$1 n=$2 dst=$3
    if [ "$backend" = dind ]; then
        incus file push "$src" "$labvm/root/xfer" >/dev/null && incus exec "$labvm" -- docker cp /root/xfer "n$n:$dst" >/dev/null
    else
        incus file push "$src" "swarm-$n$dst" >/dev/null
    fi
}
say() { printf '\n=== %s\n' "$*"; }
proxy_ctr() { on "$1" "{ docker ps -q -f name=^ocel-proxy-sa\$ -f status=running; docker ps -q -f label=com.docker.swarm.service.name=ocel-proxy -f status=running; } | head -1"; }

load_images() {
    for n in "$@"; do
        on "$n" 'mkdir -p /root'
        push "$lab/app.tar" "$n" /root/app.tar
        push "$lab/caddy.tar" "$n" /root/caddy.tar
        on "$n" 'docker load -q -i /root/app.tar >/dev/null && docker load -q -i /root/caddy.tar >/dev/null && echo "$(hostname): images loaded"'
    done
}

caddy_config() {
    local upstream=$1 mode=${2:-static}
    local proxy retry="\"load_balancing\":{\"try_duration\":\"5s\",\"try_interval\":\"100ms\"},"
    case "$mode" in *-noretry) retry="" ;; esac
    case "$mode" in
    dynamic*)
        proxy="{\"handler\":\"reverse_proxy\",\"dynamic_upstreams\":{\"source\":\"a\",\"name\":\"$upstream\",\"port\":\"8080\",\"refresh\":\"1s\"},$retry\"health_checks\":{\"passive\":{\"fail_duration\":\"10s\"}}}"
        ;;
    *)
        proxy="{\"handler\":\"reverse_proxy\",\"upstreams\":[{\"dial\":\"$upstream:8080\"}],$retry\"health_checks\":{\"active\":{\"uri\":\"/health\",\"interval\":\"1s\",\"timeout\":\"1s\"},\"passive\":{\"fail_duration\":\"10s\"}}}"
        ;;
    esac
    cat <<EOF
{"admin":{"listen":"localhost:2019"},"apps":{"http":{"servers":{"ocel":{"listen":[":80"],"automatic_https":{"disable":true},"routes":[{"match":[{"host":["$domain"]}],"handle":[$proxy]}]}}}}}
EOF
}

route() {
    local node=$1 upstream=$2 mode=${3:-static}
    caddy_config "$upstream" "$mode" > "$lab/caddy.json"
    push "$lab/caddy.json" "$node" /var/lib/ocel-proto/proxy/caddy.json
    local ctr
    ctr=$(proxy_ctr "$node")
    on "$node" "docker exec $ctr caddy reload --config /etc/ocel/caddy.json 2>&1 | tail -1" || true
    echo "swarm-$node routes $domain -> $upstream ($mode)"
}

converged() {
    local node=$1 service=$2 want=$3 limit=${4:-90}
    for _ in $(seq 1 "$limit"); do
        local running
        running=$(on "$node" "docker service ps $service -f desired-state=running --format '{{.CurrentState}}' 2>/dev/null | grep -c '^Running'")
        [ "$running" -ge "$want" ] && { echo "$service: $running/$want running"; return 0; }
        sleep 1
    done
    echo "$service: did not converge"; on "$node" "docker service ps --no-trunc $service | head"; return 1
}

load() {
    "$lab/loadgen" -url "http://$(entry)/${LOADPATH:-}" -host "$domain" -c "${2:-20}" -d "${1:-30s}"
}

placement() {
    on 1 "docker service ls --format '{{.Name}} {{.Replicas}}'; for s in \$(docker service ls -q); do docker service ps \$s -f desired-state=running --format '{{.Name}} {{.Node}} {{.CurrentState}}'; done"
}

release_stack() {
    local release=$1 version=$2 replicas=$3 secret=${4:-}
    local constraint="node.labels.ocel.pool.web == true"
    local secrets_block="" service_secrets=""
    if [ -n "$secret" ]; then
        secrets_block=$'secrets:\n  '"$secret"$':\n    external: true'
        service_secrets=$'    secrets:\n      - source: '"$secret"$'\n        target: app_secret'
    fi
    cat <<EOF
services:
  web:
    image: $(on 1 "docker image inspect ocel-proto/app:$version --format '{{index .RepoDigests 0}}'")
    environment:
      VERSION: $version
      NODE: "{{.Node.Hostname}}"
    networks: [ocel-demo]
$service_secrets
    deploy:
      replicas: $replicas
      placement:
        constraints: ["$constraint"]
        preferences: [{spread: node.labels.ocel.machine}]
      update_config: {order: start-first, parallelism: 1, failure_action: rollback, monitor: 5s}
      rollback_config: {order: start-first, parallelism: 1}
      restart_policy: {condition: any, delay: 1s}
networks:
  ocel-demo:
    external: true
$secrets_block
EOF
}

deploy_release() {
    local release=$1; shift
    release_stack "$release" "$@" > "$lab/stack-$release.yml"
    push "$lab/stack-$release.yml" 1 /root/stack-$release.yml
    on 1 "s=\$(date +%s); docker stack deploy --detach=false --resolve-image never -c /root/stack-$release.yml demo--web--$release > /tmp/deploy.log 2>&1; rc=\$?; tail -3 /tmp/deploy.log; echo \"stack deploy exit=\$rc took=\$((\$(date +%s)-s))s\""
}

case "${1:-}" in
reset)
    for n in 1 2 3; do
        on "$n" 'docker swarm leave --force >/dev/null 2>&1; docker rm -f $(docker ps -aq) >/dev/null 2>&1; docker volume prune -af >/dev/null 2>&1; docker network prune -f >/dev/null; echo "$(hostname) reset"'
    done
    ;;
images) shift; load_images "${@:-1 2 3}" ;;
init)
    say "one machine: swarm init with explicit advertise address and address pool"
    on 1 "docker swarm init --advertise-addr $(addr 1) --default-addr-pool 10.211.0.0/16 --default-addr-pool-mask-length 24 >/dev/null && docker info --format 'swarm={{.Swarm.LocalNodeState}} managers={{.Swarm.Managers}} nodes={{.Swarm.Nodes}}'"
    on 1 'docker node update --label-add ocel.machine=$(hostname) --label-add ocel.ingress=true --label-add ocel.pool.web=true $(hostname) >/dev/null'
    on 1 'docker network create -d overlay --attachable --opt com.docker.network.driver.mtu=1450 ocel-demo >/dev/null && echo network ocel-demo created'
    ;;
proxy)
    say "ingress: caddy as a global service on ingress-labelled machines, host-mode ports"
    for n in 1 2 3; do on "$n" 'mkdir -p /var/lib/ocel-proto/proxy'; done
    caddy_config "demo--web--r1_web" > "$lab/caddy.json"
    for n in 1 2 3; do push "$lab/caddy.json" "$n" /var/lib/ocel-proto/proxy/caddy.json 2>/dev/null; done
    on 1 'docker service create -q --name ocel-proxy --mode global --constraint node.labels.ocel.ingress==true \
        --publish mode=host,published=80,target=80 --network ocel-demo \
        --mount type=bind,src=/var/lib/ocel-proto/proxy,dst=/etc/ocel \
        ocel-proto/caddy:pinned caddy run --config /etc/ocel/caddy.json >/dev/null'
    converged 1 ocel-proxy 1
    ;;
release)
    shift
    say "release $1: stack deploy of app $2 x$3"
    deploy_release "$@"
    ;;
route) shift; route "$@" ;;
load) shift; load "$@" ;;
placement) placement ;;
join)
    shift
    say "grow: join machines $* as workers"
    token=$(on 1 'docker swarm join-token -q worker')
    for n in "$@"; do
        on "$n" "docker swarm join --advertise-addr $(addr "$n") --token $token $(addr 1):2377 >/dev/null && echo swarm-$n joined"
        h=$(on "$n" hostname); on 1 "docker node update --label-add ocel.machine=$h --label-add ocel.pool.web=true $h >/dev/null"
    done
    on 1 "docker node ls --format '{{.Hostname}} {{.Status}} {{.Availability}} {{.ManagerStatus}}'"
    ;;
promote)
    shift
    on 1 "docker node promote $* >/dev/null; docker node ls --format '{{.Hostname}} {{.Status}} {{.ManagerStatus}}'"
    ;;
on) shift; n=$1; shift; on "$n" "$*" ;;
tasks)
    on 1 "docker service ps $2 --format '{{.Name}} {{.ID}} {{.Node}} desired={{.DesiredState}} {{.CurrentState}} {{.Error}}' | head -${3:-12}; docker service inspect $2 --format 'version={{.Version.Index}} update={{if .UpdateStatus}}{{.UpdateStatus.State}}{{end}}'"
    ;;
idempotent)
    say "redeploy an unchanged release stack: do tasks restart?"
    before=$(on 1 "docker service ps demo--web--$2_web -q -f desired-state=running | sort | tr '\n' ' '")
    deploy_release "$2" "$3" "$4"
    after=$(on 1 "docker service ps demo--web--$2_web -q -f desired-state=running | sort | tr '\n' ' '")
    echo "before: $before"; echo "after:  $after"
    [ "$before" = "$after" ] && echo "RESULT: no restart" || echo "RESULT: tasks replaced"
    ;;
bluegreen)
    from=$2 to=$3 version=$4 replicas=$5
    say "blue/green: release $to ($version x$replicas) under load, flip caddy, retire $from"
    load 60s > "$lab/load.out" 2>&1 &
    sleep 3
    deploy_release "$to" "$version" "$replicas"
    route 1 "demo--web--${to}_web"
    sleep 3
    on 1 "docker service scale -d demo--web--${from}_web=0 >/dev/null && echo retired $from"
    wait
    cat "$lab/load.out"
    ;;
rolling)
    version=$2 replicas=$3 mode=${4:-static}
    say "rolling: start-first update of one stable service to $version under load, caddy -> $mode"
    upstream=demo--web--stable_web
    case "$mode" in dynamic*) upstream=tasks.demo--web--stable_web ;; esac
    route 1 "$upstream" "$mode"
    sleep 2
    load 70s > "$lab/load.out" 2>&1 &
    sleep 3
    deploy_release stable "$version" "$replicas"
    wait
    cat "$lab/load.out"
    ;;
pullprobe)
    say "image references that skip the registry round-trip on task start"
    on 1 "docker tag ocel-proto/app:v3 ocel.invalid/app:v3"
    id=$(on 1 "docker image inspect ocel-proto/app:v3 --format '{{.Id}}'")
    digest=$(on 1 "docker image inspect ocel-proto/app:v3 --format '{{join .RepoDigests \" \"}}'")
    echo "image id: $id"; echo "repo digests: ${digest:-none}"
    for ref in ocel-proto/app:v3 ocel.invalid/app:v3 "$id" ${digest:+$digest}; do
        name=probe-$RANDOM
        s=$(date +%s)
        on 1 "docker service create -q --detach=false --name $name --no-resolve-image --network ocel-demo -e VERSION=probe $ref >/dev/null 2>&1"
        echo "ref=$ref -> running after $(( $(date +%s) - s ))s"
        on 1 "docker service rm $name >/dev/null"
    done
    on 1 "journalctl -u docker --since '-3min' --no-pager | grep -c 'pulling image failed'" | sed 's/^/pull failures logged: /'
    ;;
dind-up)
    say "three docker-in-docker machines inside $labvm"
    incus exec "$labvm" -- sh -c 'modprobe -a ip_vs ip_vs_rr xt_ipvs vxlan br_netfilter nf_conntrack 2>/dev/null; true'
    incus file push "$lab/dind.tar" "$labvm/root/dind.tar" >/dev/null
    incus exec "$labvm" -- sh -c 'docker load -q -i /root/dind.tar >/dev/null; docker network inspect lab >/dev/null 2>&1 || docker network create --subnet 172.30.0.0/24 lab >/dev/null'
    for n in 1 2 3; do
        publish=""; [ "$n" = 1 ] && publish="-p 80:80"
        incus exec "$labvm" -- sh -c "docker rm -f n$n >/dev/null 2>&1; docker run -d --privileged --name n$n --hostname n$n --network lab --ip 172.30.0.1$n $publish -e DOCKER_TLS_CERTDIR= -v n$n:/var/lib/docker docker:29.8.0-dind >/dev/null"
    done
    for n in 1 2 3; do
        for _ in $(seq 1 30); do on "$n" 'docker info >/dev/null 2>&1' && break; sleep 1; done
        on "$n" 'echo "$(hostname) docker $(docker version --format {{.Server.Version}}) kernel $(uname -r)"'
    done
    ;;
failover)
    victim=$2
    say "machine $victim dies under load"
    load 60s > "$lab/load.out" 2>&1 &
    sleep 10
    if [ "$backend" = dind ]; then incus exec "$labvm" -- docker kill "n$victim" >/dev/null; else incus stop -f "swarm-$victim"; fi
    echo "killed machine $victim at t+10s"
    for t in 5 15 30; do
        sleep $(( t == 5 ? 5 : (t == 15 ? 10 : 15) ))
        echo "--- t+$((10 + t))s"; on 1 "docker node ls --format '{{.Hostname}} {{.Status}} {{.ManagerStatus}}'"; placement | grep -v ocel-proxy
    done
    wait
    cat "$lab/load.out"
    ;;
revive)
    if [ "$backend" = dind ]; then incus exec "$labvm" -- docker start "n$2" >/dev/null; else incus start "swarm-$2"; fi
    for _ in $(seq 1 60); do on 1 "docker node ls --format '{{.Hostname}} {{.Status}}'" | grep -q "$(on "$2" hostname 2>/dev/null) Ready" && break; sleep 1; done
    on 1 "docker node ls --format '{{.Hostname}} {{.Status}} {{.ManagerStatus}}'"
    ;;
restart-ingress)
    say "the ingress machine's docker daemon restarts (reboot stand-in) under load"
    load 60s > "$lab/load.out" 2>&1 &
    sleep 5
    s=$(date +%s)
    if [ "$backend" = dind ]; then incus exec "$labvm" -- docker restart n1 >/dev/null; else on 1 'systemctl restart docker'; fi
    for _ in $(seq 1 90); do
        curl -s -o /dev/null -w '%{http_code}' -H "Host: $domain" "http://$(entry)/" 2>/dev/null | grep -q 200 && break
        sleep 1
    done
    echo "serving again $(( $(date +%s) - s ))s after the restart began"
    wait
    cat "$lab/load.out"
    ;;
secret)
    shift
    name=$1 value=$2 release=$3 version=$4 replicas=$5
    say "secret $name=$value delivered to release $release"
    on 1 "printf %s $value | docker secret create $name - >/dev/null && echo created $name"
    deploy_release "$release" "$version" "$replicas" "$name"
    for i in 1 2 3 4 5 6; do curl -s -H "Host: $domain" "http://$(entry)/"; done | sort | uniq -c
    ;;
stateful)
    say "stateful service pinned to machine $2 with a node-local volume"
    h=$(on "$2" hostname)
    on 1 "docker service create -q --detach=false --name demo--infra-pg --constraint node.labels.ocel.machine==$h --mount type=volume,src=pgdata,dst=/data --update-order stop-first --network ocel-demo -e VERSION=pg \$(docker image inspect ocel-proto/app:v1 --format '{{index .RepoDigests 0}}') >/dev/null"
    ./lab.sh tasks demo--infra-pg 3
    ;;
broken)
    say "release a version whose healthcheck never passes onto the stable service"
    release_stack stable v3 "$2" | sed 's/      VERSION: v3/      VERSION: broken\n      BROKEN: "1"/' > "$lab/stack-broken.yml"
    push "$lab/stack-broken.yml" 1 /root/stack-broken.yml
    on 1 "s=\$(date +%s); timeout 300 docker stack deploy --detach=false --resolve-image never -c /root/stack-broken.yml demo--web--stable > /tmp/deploy.log 2>&1; rc=\$?; tail -4 /tmp/deploy.log; echo \"stack deploy exit=\$rc took=\$((\$(date +%s)-s))s\""
    on 1 "docker service inspect demo--web--stable_web --format 'UpdateStatus={{.UpdateStatus.State}} msg={{.UpdateStatus.Message}}'"
    for i in 1 2 3; do curl -s -H "Host: $domain" "http://$(entry)/"; done | sort | uniq -c
    ;;
netadd)
    say "a new project network is added to the proxy service under load"
    load 40s > "$lab/load.out" 2>&1 &
    sleep 5
    on 1 "docker network create -d overlay --attachable ocel-other-$RANDOM" > "$lab/net.id"
    on 1 "docker service update -q --detach=false --network-add \$(cat /dev/null)$(cat "$lab/net.id") ocel-proxy >/dev/null 2>&1; echo proxy service updated"
    ./lab.sh tasks ocel-proxy 4
    wait
    cat "$lab/load.out"
    ;;
proxy-digest)
    on 1 "docker service update -q --detach=false --image \$(docker image inspect ocel-proto/caddy:pinned --format '{{index .RepoDigests 0}}') ocel-proxy >/dev/null 2>&1; docker service inspect ocel-proxy --format '{{.Spec.TaskTemplate.ContainerSpec.Image}}'"
    ;;
standalone)
    say "ingress: caddy as a plain container on the ingress machine, joined to the attachable overlay"
    on 1 "docker service rm ocel-proxy >/dev/null 2>&1; sleep 3; docker run -d --name ocel-proxy-sa --restart unless-stopped -p 80:80 --network ocel-demo -v /var/lib/ocel-proto/proxy:/etc/ocel \$(docker image inspect ocel-proto/caddy:pinned --format '{{index .RepoDigests 0}}') caddy run --config /etc/ocel/caddy.json >/dev/null && echo started"
    sleep 2
    route 1 "${2:-demo--web--stable_web}"
    ;;
netadd-standalone)
    say "a new project network is connected to the standalone proxy under load"
    load 30s > "$lab/load.out" 2>&1 &
    sleep 5
    on 1 "n=ocel-other-\$RANDOM; docker network create -d overlay --attachable \$n >/dev/null && docker network connect \$n ocel-proxy-sa && echo connected \$n; docker inspect ocel-proxy-sa --format '{{.State.StartedAt}} networks={{len .NetworkSettings.Networks}}'"
    wait
    cat "$lab/load.out"
    ;;
*) echo "usage: lab.sh reset|images|init|proxy|release|route|load|placement|join|promote|dind-up|failover|revive|restart-ingress|secret|stateful|broken|netadd"; exit 2 ;;
esac
