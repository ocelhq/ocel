#!/bin/sh
set -eu
cd /home/vndaba/Dev/ocelhq/.claude/worktrees/vps-bucket-918
export OCEL_VPS_HOST=10.160.227.2
export OCEL_VPS_USER=ubuntu
export OCEL_VPS_IDENTITY_FILE=/home/vndaba/.local/state/ocel-incus/id_ed25519
dir=tests/journeys/output/vps/sweep-918
mkdir -p "$dir"
cat > "$dir/ocel.json" <<JSON
{
  "slug": "j-local-vndaba-sdk-node",
  "provider": {
    "name": "vps",
    "options": {
      "ssh": { "host": "10.160.227.2", "user": "ocel-deploy", "identityFile": "/home/vndaba/.local/state/ocel-incus/id_ed25519" }
    }
  },
  "apps": []
}
JSON
cd "$dir"
OCEL_VPS_USER=ocel-deploy exec /home/vndaba/Dev/ocelhq/.claude/worktrees/vps-bucket-918/dist/ocel_linux_amd64_v1/ocel destroy production --yes
