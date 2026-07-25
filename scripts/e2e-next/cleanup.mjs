#!/usr/bin/env node
// NEXT_TEST_CLEANUP_SCRIPT_PATH for the Next.js deployment-adapter
// compatibility harness. Runs with cwd set to the temp app after its tests have
// finished.
//
// This is the ONLY footprint control in the system: nothing sweeps orphans
// later, so every deploy this suite made has to be torn down here. It therefore
// never skips on partial state — a deploy that failed halfway still provisioned
// Lambdas, a worker script and a DNS label — and it blocks until teardown
// returns, failing loudly (non-zero, which the harness surfaces) if it cannot
// finish.

import { spawnSync } from "node:child_process";
import { readFileSync } from "node:fs";
import { join } from "node:path";

import { STATE_FILE, previewSlug } from "./lib.mjs";

const TEARDOWN_TIMEOUT_MS = 20 * 60 * 1000;

const appDir = process.cwd();
const adapterDir = process.env.ADAPTER_DIR;
if (!adapterDir) {
  console.error("[ocel-e2e] cleanup cannot run: ADAPTER_DIR is not set");
  process.exit(1);
}

const slug = resolveSlug();
console.error(`[ocel-e2e] tearing down preview ${slug}`);

const res = spawnSync(
  process.execPath,
  [join(adapterDir, "packages", "ocel", "bin", "run.js"), "preview", "rm", "--name", slug, "--yes"],
  { cwd: appDir, stdio: ["ignore", "inherit", "inherit"], timeout: TEARDOWN_TIMEOUT_MS },
);

if (res.error || res.signal || res.status !== 0) {
  const why = res.error?.message ?? (res.signal ? `killed with ${res.signal}` : `exited with ${res.status}`);
  console.error(
    `[ocel-e2e] TEARDOWN FAILED for preview ${slug}: ${why}\n` +
      `[ocel-e2e] its Lambdas, worker script and DNS label are still live; ` +
      `remove them with \`ocel preview rm --name ${slug} --yes\``,
  );
  process.exit(1);
}

console.error(`[ocel-e2e] preview ${slug} torn down`);

// resolveSlug prefers the slug deploy.mjs persisted, but re-derives it the same
// way deploy.mjs did when the state file is missing or unreadable: a deploy that
// died before writing it may still have provisioned infrastructure, and this is
// the only chance to reclaim it.
function resolveSlug() {
  try {
    const state = JSON.parse(readFileSync(join(appDir, STATE_FILE), "utf8"));
    if (state.slug) {
      return state.slug;
    }
    console.error(`[ocel-e2e] ${STATE_FILE} carries no slug; re-deriving it`);
  } catch {
    console.error(`[ocel-e2e] no readable ${STATE_FILE}; re-deriving the slug`);
  }
  return previewSlug({ runId: process.env.GITHUB_RUN_ID, dir: process.env.NEXT_TEST_DIR || appDir });
}
