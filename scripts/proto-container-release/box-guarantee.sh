#!/usr/bin/env bash
set -euo pipefail

here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
# shellcheck source=/dev/null
source "$here/lib.sh"

DEPLOY_USER=ocel-deploy
DEPLOY_HOME=/var/lib/ocel

log "asserting the bootstrap guarantee on $OCEL_INCUS_ADDR"

if ! box "command -v docker >/dev/null"; then
    note "installing the engine from https://get.docker.com"
    box "curl -fsSL --retry 5 https://get.docker.com | sudo sh >/dev/null"
fi
box "sudo systemctl enable --now docker.service"

box "getent passwd $DEPLOY_USER >/dev/null || sudo useradd -m -d $DEPLOY_HOME -s /bin/sh -G docker $DEPLOY_USER"
box "sudo usermod -aG docker $DEPLOY_USER"
box "sudo install -d -m 700 -o $DEPLOY_USER -g $DEPLOY_USER $DEPLOY_HOME/.ssh"
box "sudo cp /home/$OCEL_INCUS_USER/.ssh/authorized_keys $DEPLOY_HOME/.ssh/authorized_keys"
box "sudo chown $DEPLOY_USER:$DEPLOY_USER $DEPLOY_HOME/.ssh/authorized_keys"
box "sudo chmod 600 $DEPLOY_HOME/.ssh/authorized_keys"

note "guarantee stands: engine serving, $DEPLOY_USER in the docker group"
