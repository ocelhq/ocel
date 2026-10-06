#!/usr/bin/env node

import { spawnSync } from "node:child_process";
import { mkdtempSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { pathToFileURL } from "node:url";

import { listProjectSlugs, readAccessToken } from "./gcp.mjs";
import {
  isProductionTarget,
  ocelBinary,
  projectSlugForRun,
  readCompatTarget,
  renderOcelConfig,
  requireNamespace,
  selectRunSlugs,
  withoutSkipDriftChecks,
} from "./lib.mjs";
import { linkSidecar } from "./sidecar.mjs";

const TEARDOWN_TIMEOUT_MS = 30 * 60 * 1000;

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  process.exit(await main(process.argv[2]));
}

async function main(named) {
  requireNamespace();
  const target = readCompatTarget();
  if (named) {
    return destroyProject(named, target) ? 0 : 1;
  }
  if (!isProductionTarget(target)) {
    return destroyProject(projectSlugForRun(), target) ? 0 : 1;
  }
  const run = projectSlugForRun();
  const slugs = selectRunSlugs(
    await listProjectSlugs({
      ...target.gcp,
      namespace: process.env.OCEL_NAMESPACE,
      token: readAccessToken(),
    }),
    run,
  );
  if (slugs.length === 0) {
    console.error(`[ocel-e2e] nothing of ${run} is left`);
    return 0;
  }
  const failed = slugs.filter((slug) => !destroyProject(slug, target));
  return failed.length === 0 ? 0 : 1;
}

export function destroyProject(slug, target = readCompatTarget()) {
  requireNamespace();
  const adapterDir = process.env.ADAPTER_DIR;
  const sidecarDir = process.env.OCEL_E2E_SIDECAR_DIR;
  if (!adapterDir || !sidecarDir) {
    throw new Error("project-teardown needs ADAPTER_DIR and OCEL_E2E_SIDECAR_DIR");
  }

  const dir = mkdtempSync(join(tmpdir(), `ocel-e2e-teardown-${slug}-`));
  writeFileSync(join(dir, "ocel.config.ts"), renderOcelConfig({ slug, target }));
  linkSidecar(dir, sidecarDir);

  const tier = isProductionTarget(target) ? "production" : "preview";
  console.error(`[ocel-e2e] destroying the ${tier} footprint of project ${slug} (from ${dir})`);
  const res = spawnSync(process.execPath, [ocelBinary(adapterDir), "destroy", tier, "--yes"], {
    cwd: dir,
    stdio: ["ignore", "inherit", "inherit"],
    timeout: TEARDOWN_TIMEOUT_MS,
    env: withoutSkipDriftChecks(process.env),
  });

  if (res.error || res.signal || res.status !== 0) {
    const why =
      res.error?.message ??
      (res.signal ? `killed with ${res.signal}` : `exited with ${res.status}`);
    console.error(
      `[ocel-e2e] PROJECT TEARDOWN FAILED for ${slug}: ${why}\n` +
        `[ocel-e2e] its preview footprint is still billing — store instance, staged ` +
        `deployments and assets — and the slug stays taken. Other projects keep deploying ` +
        `onto the bootstrap's preview domain regardless. Retry with ` +
        `\`node tests/next-compat/project-teardown.mjs ${slug}\`.`,
    );
    return false;
  }
  console.error(`[ocel-e2e] project ${slug} destroyed`);
  return true;
}
