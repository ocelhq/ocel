#!/usr/bin/env node
// NEXT_TEST_CLEANUP_SCRIPT_PATH for the Next.js deployment-adapter
// compatibility harness. Runs with cwd set to the temp app after its tests have
// finished.
//
// It removes ONE ephemeral preview pointer — this temp app's — from the run's
// shared project, and nothing else: the project, its root stack, its entrypoint
// worker and the wildcard route they hold are the whole run's and outlive every
// fixture. Project-level teardown is the workflow's `destroy` job
// (project-teardown.mjs), which is also what reclaims a run this never ran for.
//
// It still never skips on partial state — a deploy that failed halfway still
// provisioned Lambdas and a stack — and it blocks until teardown returns,
// failing loudly (non-zero, which the harness surfaces) if it cannot finish.

import { spawnSync } from "node:child_process";
import { existsSync, readFileSync, writeFileSync } from "node:fs";
import { join } from "node:path";

import { STATE_FILE, previewRefForApp, projectSlugForRun, renderOcelConfig } from "./lib.mjs";

const TEARDOWN_TIMEOUT_MS = 20 * 60 * 1000;

const appDir = process.cwd();
const adapterDir = process.env.ADAPTER_DIR;
if (!adapterDir) {
  console.error("[ocel-e2e] cleanup cannot run: ADAPTER_DIR is not set");
  process.exit(1);
}

const { slug, ref } = resolveIdentity();
ensureConfig(slug);
console.error(`[ocel-e2e] removing preview ${ref} from project ${slug}`);

const res = spawnSync(
  process.execPath,
  [join(adapterDir, "packages", "ocel", "bin", "run.js"), "preview", "rm", "--ref", ref, "--yes"],
  { cwd: appDir, stdio: ["ignore", "inherit", "inherit"], timeout: TEARDOWN_TIMEOUT_MS },
);

if (res.error || res.signal || res.status !== 0) {
  const why = res.error?.message ?? (res.signal ? `killed with ${res.signal}` : `exited with ${res.status}`);
  console.error(
    `[ocel-e2e] TEARDOWN FAILED for preview ${ref} of project ${slug}: ${why}\n` +
      `[ocel-e2e] its Lambdas and stacks are still live; remove them by running ` +
      `\`ocel preview rm --ref ${ref} --yes\` from a directory whose ocel.config.ts ` +
      `declares slug: "${slug}", or take the whole project with ` +
      `\`node scripts/e2e-next/project-teardown.mjs ${slug}\``,
  );
  process.exit(1);
}

console.error(`[ocel-e2e] preview ${ref} removed`);

// resolveIdentity prefers what deploy.mjs persisted, but re-derives each half
// the same way deploy.mjs did when the state file is missing or unreadable: a
// deploy that died before writing it may still have provisioned infrastructure,
// and this is the only chance to reclaim it before the run's destroy job takes
// the entire project.
function resolveIdentity() {
  let state = {};
  try {
    state = JSON.parse(readFileSync(join(appDir, STATE_FILE), "utf8")) ?? {};
  } catch {
    console.error(`[ocel-e2e] no readable ${STATE_FILE}; re-deriving the project slug and preview ref`);
  }
  return {
    slug: state.slug || projectSlugForRun(),
    ref: state.ref || previewRefForApp(appDir),
  };
}

// ensureConfig restores the ocel.config.ts `preview rm` resolves the project
// through. A deploy that died before writing it, or a directory the harness
// partly wiped, would otherwise leave teardown with nothing to address — and the
// config is pure, so re-rendering it from the same environment reproduces it.
function ensureConfig(slug) {
  const path = join(appDir, "ocel.config.ts");
  if (existsSync(path)) {
    return;
  }
  console.error(`[ocel-e2e] no ocel.config.ts; re-rendering it for project ${slug}`);
  writeFileSync(path, renderOcelConfig({ slug, previewDomain: process.env.OCEL_E2E_PREVIEW_DOMAIN || "" }));
}
