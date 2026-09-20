#!/bin/sh
set -eu
cd /home/vndaba/Dev/ocelhq/.claude/worktrees/vps-bucket-918
export OCEL_VPS_HOST=10.160.227.2
export OCEL_VPS_USER=ubuntu
export OCEL_VPS_IDENTITY_FILE=/home/vndaba/.local/state/ocel-incus/id_ed25519
export OCEL_JOURNEY_FIXTURES="${FIXTURES:-sdk/node}"
export OCEL_JOURNEY_COVERAGE=every-cell
exec pnpm --filter @ocel-tests/journeys journey:vps
