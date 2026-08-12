#!/usr/bin/env node

import { spawnSync } from "node:child_process";
import { mkdtempSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { pathToFileURL } from "node:url";

import { projectSlugForRun, renderOcelConfig, withoutSkipDriftChecks } from "./lib.mjs";
import { assertToolchain, linkOcel } from "./toolchain.mjs";

const TEARDOWN_TIMEOUT_MS = 30 * 60 * 1000;

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  const slug = process.argv[2] || projectSlugForRun();
  process.exit(destroyProject(slug) ? 0 : 1);
}

export function destroyProject(slug) {
  const adapterDir = process.env.ADAPTER_DIR;
  if (!adapterDir) {
    throw new Error("project-teardown needs ADAPTER_DIR");
  }
  assertToolchain(adapterDir);

  const dir = mkdtempSync(join(tmpdir(), `ocel-e2e-node-teardown-${slug}-`));
  writeFileSync(join(dir, "ocel.config.ts"), renderOcelConfig({ slug }));
  linkOcel(dir, adapterDir);

  console.error(`[ocel-e2e-node] destroying the preview footprint of project ${slug} (from ${dir})`);
  const res = spawnSync(
    process.execPath,
    [join(adapterDir, "packages", "ocel", "bin", "run.js"), "destroy", "--preview", "--yes"],
    {
      cwd: dir,
      stdio: ["ignore", "inherit", "inherit"],
      timeout: TEARDOWN_TIMEOUT_MS,
      env: withoutSkipDriftChecks(process.env),
    },
  );

  if (res.error || res.signal || res.status !== 0) {
    const why = res.error?.message ?? (res.signal ? `killed with ${res.signal}` : `exited with ${res.status}`);
    console.error(
      `[ocel-e2e-node] PROJECT TEARDOWN FAILED for ${slug}: ${why}\n` +
        `[ocel-e2e-node] its preview footprint is still billing — staged deployments and assets — and the ` +
        `slug stays taken. Retry with \`node scripts/e2e-node/project-teardown.mjs ${slug}\`.`,
    );
    return false;
  }
  console.error(`[ocel-e2e-node] project ${slug} destroyed`);
  return true;
}
