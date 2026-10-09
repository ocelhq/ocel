#!/usr/bin/env node

import { spawn } from "node:child_process";
import { mkdtempSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { setTimeout as sleep } from "node:timers/promises";
import { pathToFileURL } from "node:url";

import { listProjectSlugs, readAccessToken } from "./gcp.mjs";
import {
  heldLeaseExpiry,
  heldLeaseWaitMs,
  isProductionTarget,
  LEASE_TTL_MS,
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
    return (await destroyProject(named, target)) ? 0 : 1;
  }
  if (!isProductionTarget(target)) {
    return (await destroyProject(projectSlugForRun(), target)) ? 0 : 1;
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
  const failed = [];
  for (const slug of slugs) {
    if (!(await destroyProject(slug, target))) {
      failed.push(slug);
    }
  }
  return failed.length === 0 ? 0 : 1;
}

export async function destroyProject(slug, target = readCompatTarget()) {
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
  let deadline;
  for (;;) {
    console.error(`[ocel-e2e] destroying the ${tier} footprint of project ${slug} (from ${dir})`);
    const res = await runDestroy(adapterDir, tier, dir);
    if (!res.error && !res.signal && res.status === 0) {
      console.error(`[ocel-e2e] project ${slug} destroyed`);
      return true;
    }
    deadline ??= Date.now() + LEASE_TTL_MS + 60_000;
    const wait = heldLeaseWaitMs(res.output, { now: Date.now(), deadline });
    if (wait === null) {
      reportTeardownFailure(slug, res, deadline);
      return false;
    }
    console.error(
      `[ocel-e2e] an interrupted deploy's lease still holds ${slug}; destroying again in ${Math.ceil(wait / 1000)}s, once it runs out`,
    );
    await sleep(wait);
  }
}

function runDestroy(adapterDir, tier, dir) {
  return new Promise((resolve) => {
    const child = spawn(process.execPath, [ocelBinary(adapterDir), "destroy", tier, "--yes"], {
      cwd: dir,
      stdio: ["ignore", "pipe", "pipe"],
      timeout: TEARDOWN_TIMEOUT_MS,
      env: withoutSkipDriftChecks(process.env),
    });
    let output = "";
    child.stdout.on("data", (chunk) => {
      output += chunk;
      process.stdout.write(chunk);
    });
    child.stderr.on("data", (chunk) => {
      output += chunk;
      process.stderr.write(chunk);
    });
    child.on("error", (error) => resolve({ error, output }));
    child.on("close", (status, signal) => resolve({ status, signal, output }));
  });
}

function reportTeardownFailure(slug, res, deadline) {
  const why =
    res.error?.message ?? (res.signal ? `killed with ${res.signal}` : `exited with ${res.status}`);
  console.error(
    `[ocel-e2e] PROJECT TEARDOWN FAILED for ${slug}: ${why}` +
      (heldLeaseExpiry(res.output) !== null
        ? ` — a deploy's lease still held it at the deadline of ${new Date(deadline).toISOString()}, so that deploy is still renewing it`
        : "") +
      `\n[ocel-e2e] its preview footprint is still billing — store instance, staged ` +
      `deployments and assets — and the slug stays taken. Other projects keep deploying ` +
      `onto the bootstrap's preview domain regardless. Retry with ` +
      `\`node tests/next-compat/project-teardown.mjs ${slug}\`.`,
  );
}
