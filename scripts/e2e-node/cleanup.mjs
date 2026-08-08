#!/usr/bin/env node
// Tears down one run's deployment. Run with cwd set to the deployed app's
// directory — deploy.mjs prints it to stderr as "staged smoke app in <dir>"
// — after assertions have finished.
//
// Same footprint-control contract as scripts/e2e-next/cleanup.mjs: this is
// the ONLY thing that removes what a run provisioned, so it never skips on
// partial state and fails loudly (non-zero) if teardown cannot finish. See
// that file's own header for the full reasoning; it applies unchanged here.

import { spawnSync } from "node:child_process";
import { existsSync, readFileSync, writeFileSync } from "node:fs";
import { join } from "node:path";

import { STATE_FILE, projectSlugForApp, renderOcelConfig } from "./lib.mjs";

const TEARDOWN_TIMEOUT_MS = 10 * 60 * 1000;

const appDir = process.cwd();
const adapterDir = process.env.ADAPTER_DIR;
if (!adapterDir) {
  console.error("[ocel-e2e-node] cleanup cannot run: ADAPTER_DIR is not set");
  process.exit(1);
}

const slug = resolveSlug();
ensureConfig(slug);
console.error(`[ocel-e2e-node] tearing down project ${slug}`);

const res = spawnSync(
  process.execPath,
  [join(adapterDir, "packages", "ocel", "bin", "run.js"), "preview", "rm", "--name", slug, "--yes"],
  { cwd: appDir, stdio: ["ignore", "inherit", "inherit"], timeout: TEARDOWN_TIMEOUT_MS },
);

if (res.error || res.signal || res.status !== 0) {
  const why = res.error?.message ?? (res.signal ? `killed with ${res.signal}` : `exited with ${res.status}`);
  console.error(
    `[ocel-e2e-node] TEARDOWN FAILED for project ${slug}: ${why}\n` +
      `[ocel-e2e-node] its Lambda, worker scripts (if any) and DNS label are still live; ` +
      `remove them by running \`ocel preview rm --name ${slug} --yes\` from a ` +
      `directory whose ocel.config.ts declares slug: "${slug}"`,
  );
  process.exit(1);
}

console.error(`[ocel-e2e-node] project ${slug} torn down`);

// resolveSlug prefers the slug deploy.mjs persisted, falling back to
// re-deriving it from appDir the same way deploy.mjs did — see
// scripts/e2e-next/cleanup.mjs's own resolveSlug for why this is the last
// chance to reclaim a deploy that died before writing the state file.
function resolveSlug() {
  try {
    const state = JSON.parse(readFileSync(join(appDir, STATE_FILE), "utf8"));
    if (state.slug) {
      return state.slug;
    }
    console.error(`[ocel-e2e-node] ${STATE_FILE} carries no slug; re-deriving it`);
  } catch {
    console.error(`[ocel-e2e-node] no readable ${STATE_FILE}; re-deriving the slug`);
  }
  return projectSlugForApp(appDir);
}

function ensureConfig(slug) {
  const path = join(appDir, "ocel.config.ts");
  if (existsSync(path)) {
    return;
  }
  console.error(`[ocel-e2e-node] no ocel.config.ts; re-rendering it for project ${slug}`);
  writeFileSync(path, renderOcelConfig({ slug, previewDomain: process.env.OCEL_E2E_PREVIEW_DOMAIN || "" }));
}
