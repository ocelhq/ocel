#!/usr/bin/env node

import { spawnSync } from "node:child_process";
import { existsSync, readFileSync, writeFileSync } from "node:fs";
import { join } from "node:path";

import {
  SKIP_DRIFT_CHECK_ENV,
  STATE_FILE,
  previewRefForApp,
  projectSlugForRun,
  renderOcelConfig,
} from "./lib.mjs";

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
  {
    cwd: appDir,
    stdio: ["ignore", "inherit", "inherit"],
    timeout: TEARDOWN_TIMEOUT_MS,
    env: { ...process.env, ...SKIP_DRIFT_CHECK_ENV },
  },
);

if (res.error || res.signal || res.status !== 0) {
  const why = res.error?.message ?? (res.signal ? `killed with ${res.signal}` : `exited with ${res.status}`);
  console.error(
    `[ocel-e2e] TEARDOWN FAILED for preview ${ref} of project ${slug}: ${why}\n` +
      `[ocel-e2e] its Lambdas and stacks are still live; remove them by running ` +
      `\`ocel preview rm --ref ${ref} --yes\` from a directory whose ocel.config.ts ` +
      `declares slug: "${slug}", or take the whole project with ` +
      `\`node tests/e2e-next.js/project-teardown.mjs ${slug}\``,
  );
  process.exit(1);
}

console.error(`[ocel-e2e] preview ${ref} removed`);

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

function ensureConfig(slug) {
  const path = join(appDir, "ocel.config.ts");
  if (existsSync(path)) {
    return;
  }
  console.error(`[ocel-e2e] no ocel.config.ts; re-rendering it for project ${slug}`);
  writeFileSync(path, renderOcelConfig({ slug }));
}
