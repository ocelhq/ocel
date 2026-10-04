#!/usr/bin/env bash
# Captures real NDJSON run-event lines from the ocel binary, with no cloud credentials:
# a VPS deploy aimed at a closed loopback port with no providers installed fails fast.
set -uo pipefail
here="$(cd "$(dirname "$0")" && pwd)"
repo="$(cd "$here/../.." && pwd)"
work="$here/.work"
bin="${OCEL_BIN:-$work/ocel}"
mkdir -p "$work/home" "$here/samples"
if [ ! -x "$bin" ]; then
  (cd "$repo/cli" && go build -o "$bin" ./ocel)
fi

rm -rf "$work/fx"
cp -r "$repo/tests/fixtures/deploy/go" "$work/fx"
cp "$work/fx/ocel.vps.json" "$work/fx/ocel.json"
cd "$work/fx"
HOME="$work/home" XDG_CONFIG_HOME="$work/home/.config" \
  OCEL_VPS_HOST=127.0.0.1 OCEL_VPS_USER=nobody OCEL_VPS_IDENTITY_FILE=/dev/null \
  OCEL_PROVIDERS_DIR="$work/no-providers" \
  timeout 120 "$bin" deploy --log-format json --yes \
  >"$here/samples/deploy-vps.ndjson" 2>"$here/samples/deploy-vps.stderr"
echo "exit=$?"
