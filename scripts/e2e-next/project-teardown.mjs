#!/usr/bin/env node

import { spawnSync } from "node:child_process";
import { mkdtempSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { pathToFileURL } from "node:url";

import { projectSlugForRun, renderOcelConfig, withoutSkipDriftChecks } from "./lib.mjs";
import { linkSidecar } from "./sidecar.mjs";

const TEARDOWN_TIMEOUT_MS = 30 * 60 * 1000;

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  const slug = process.argv[2] || projectSlugForRun();
  process.exit(destroyProject(slug) ? 0 : 1);
}

export function destroyProject(slug) {
  const adapterDir = process.env.ADAPTER_DIR;
  const sidecarDir = process.env.OCEL_E2E_SIDECAR_DIR;
  if (!adapterDir || !sidecarDir) {
    throw new Error("project-teardown needs ADAPTER_DIR and OCEL_E2E_SIDECAR_DIR");
  }

  const dir = mkdtempSync(join(tmpdir(), `ocel-e2e-teardown-${slug}-`));
  writeFileSync(
    join(dir, "ocel.config.ts"),
    renderOcelConfig({ slug }),
  );
  linkSidecar(dir, sidecarDir);

  console.error(`[ocel-e2e] destroying the preview footprint of project ${slug} (from ${dir})`);
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
      `[ocel-e2e] PROJECT TEARDOWN FAILED for ${slug}: ${why}\n` +
        `[ocel-e2e] its preview footprint is still billing — store instance, staged ` +
        `deployments and assets — and the slug stays taken. Other projects keep deploying ` +
        `onto the bootstrap's preview domain regardless. Retry with ` +
        `\`node scripts/e2e-next/project-teardown.mjs ${slug}\`.`,
    );
    return false;
  }
  console.error(`[ocel-e2e] project ${slug} destroyed`);
  return true;
}
