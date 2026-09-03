#!/usr/bin/env bash
# One deploy-mode suite, N invocations in parallel. See run-suite-prompt.md.
set -uo pipefail

usage() {
  cat >&2 <<'EOF'
usage: RUN_ID=<tag> OUT_DIR=<dir> NEXT_DIR=<next.js checkout> \
       OCEL_E2E_SIDECAR_DIR=<sidecar> [ADAPTER_DIR=<repo>] \
       [STAGE=1] [ONLY='<jest -t pattern>'] run-one.sh <suite path>

Writes to $OUT_DIR/<suite name>/:
  run.log       everything jest and the deploy scripts printed
  jest.json     jest --json result
  fragment.json this suite's entry in baseline-manifest.json shape, unfiltered
  deploy.txt    every ref/slug/dir line deploy.mjs printed
  dirs.txt      every app dir this suite deployed
  status        jest's exit code
  staged.txt    STAGE=1 only: each preview left live, and how to reach it

STAGE=1 keeps the temp app dirs and the deployments. Nothing reclaims those
but you: every staged.txt is an open teardown, and a suite may deploy more
than one app.
EOF
  exit 2
}

SUITE=${1:-}
[ -n "$SUITE" ] || usage
: "${RUN_ID:?RUN_ID is required — it names the project every deploy lands in}"
: "${OUT_DIR:?OUT_DIR is required}"
: "${NEXT_DIR:?NEXT_DIR is required}"
: "${OCEL_E2E_SIDECAR_DIR:?OCEL_E2E_SIDECAR_DIR is required}"

ADAPTER_DIR=${ADAPTER_DIR:-$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)}
E2E_DIR="$ADAPTER_DIR/tests/next-compat"
NAME=$(printf '%s' "$SUITE" | sed -e 's|^test/e2e/||' -e 's|\.test\.[jt]sx*$||' -e 's|[^A-Za-z0-9]|-|g')
WORK="$OUT_DIR/$NAME"
LOG="$WORK/run.log"
mkdir -p "$WORK"
: >"$LOG"

finish() {
  local code=$? dir ref
  sed -n 's|^.*\[ocel-e2e\] preview .* in \(/.*\)$|\1|p' "$LOG" | sort -u >"$WORK/dirs.txt"
  grep '\[ocel-e2e\] preview ' "$LOG" | sort -u >"$WORK/deploy.txt" 2>/dev/null
  : >"$WORK/staged.txt"

  while read -r dir; do
    [ -n "$dir" ] && [ -f "$dir/.ocel-e2e.json" ] || continue
    ref=$(node -p "require('$dir/.ocel-e2e.json').ref")
    if [ "${STAGE:-}" = 1 ]; then
      {
        printf 'dir=%s\nref=%s\n' "$dir" "$ref"
        node -p "require('$dir/.ocel-e2e.json').slug" | sed 's/^/slug=/'
        node -p "JSON.parse(require('fs').readFileSync('$dir/.ocel/deploy-result.json')).apps.flatMap((a) => a.urls ?? [])[0]" 2>/dev/null |
          sed 's/^/url=/'
        printf 'teardown=cd %s && node %s/packages/ocel/bin/run.js preview rm --ref %s --yes\n' \
          "$dir" "$ADAPTER_DIR" "$ref"
      } >>"$WORK/staged.txt"
    else
      (cd "$dir" && node "$ADAPTER_DIR/packages/ocel/bin/run.js" preview rm --ref "$ref" --yes) >>"$LOG" 2>&1
    fi
  done <"$WORK/dirs.txt"
  [ -s "$WORK/staged.txt" ] || rm -f "$WORK/staged.txt"

  if [ -s "$WORK/jest.json" ]; then
    node --input-type=module -e "
      import { readFileSync } from 'node:fs';
      const { buildBaselineManifest } = await import('$E2E_DIR/lib.mjs');
      const results = JSON.parse(readFileSync('$WORK/jest.json', 'utf8'));
      console.log(JSON.stringify(buildBaselineManifest([{ suite: '$SUITE', results }]), null, 2));
    " >"$WORK/fragment.json" 2>>"$LOG"
  fi

  printf '%s\n' "$code" >"$WORK/status"
}
trap finish EXIT

ENVV=(
  ADAPTER_DIR="$ADAPTER_DIR"
  GITHUB_RUN_ID="$RUN_ID"
  OCEL_ACCESS_TOKEN=thisdoesntmatter
  OCEL_API_URL=https://ocel.app
  OCEL_E2E_SIDECAR_DIR="$OCEL_E2E_SIDECAR_DIR"
  OCEL_E2E_DEPLOY_TIMEOUT_MS=540000
  HEADLESS=true
  IS_TURBOPACK_TEST=1
  NEXT_ENABLE_ADAPTER=1
  NEXT_TEST_JOB=1
  NEXT_TEST_MODE=deploy
  NEXT_E2E_TEST_TIMEOUT=600000
  NEXT_TELEMETRY_DISABLED=1
  NEXT_TEST_DEPLOY_SCRIPT_PATH="$E2E_DIR/deploy.mjs"
  NEXT_TEST_DEPLOY_LOGS_SCRIPT_PATH="$E2E_DIR/logs.mjs"
)
if [ "${STAGE:-}" = 1 ]; then
  ENVV+=(NEXT_TEST_SKIP_CLEANUP=1)
else
  ENVV+=(NEXT_TEST_CLEANUP_SCRIPT_PATH="$E2E_DIR/cleanup.mjs")
fi

ARGS=(--runInBand --json --outputFile "$WORK/jest.json" "$SUITE")
[ -n "${ONLY:-}" ] && ARGS+=(-t "$ONLY")

cd "$NEXT_DIR" || exit 1
env "${ENVV[@]}" pnpm jest "${ARGS[@]}" 2>&1 | tee -a "$LOG"
exit "${PIPESTATUS[0]}"
