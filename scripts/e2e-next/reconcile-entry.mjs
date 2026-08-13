#!/usr/bin/env node

import { spawnSync } from "node:child_process";
import { mkdtempSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { pathToFileURL } from "node:url";

import { renderOcelConfig, withoutSkipDriftChecks } from "./lib.mjs";
import { linkSidecar } from "./sidecar.mjs";

const RECONCILE_TIMEOUT_MS = 10 * 60 * 1000;
const SLUG = "e2e-edge";

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  const wildcard = process.argv[2] || process.env.OCEL_E2E_PREVIEW_DOMAIN;
  process.exit(reconcileEntry(wildcard) ? 0 : 1);
}

export function reconcileEntry(wildcard) {
  const adapterDir = process.env.ADAPTER_DIR;
  const sidecarDir = process.env.OCEL_E2E_SIDECAR_DIR;
  if (!adapterDir || !sidecarDir) {
    throw new Error("reconcile-entry needs ADAPTER_DIR and OCEL_E2E_SIDECAR_DIR");
  }
  if (!wildcard) {
    throw new Error(
      "reconcile-entry needs the preview wildcard, as argv[2] or OCEL_E2E_PREVIEW_DOMAIN",
    );
  }

  const dir = mkdtempSync(join(tmpdir(), "ocel-e2e-entry-"));
  writeFileSync(join(dir, "ocel.config.ts"), renderOcelConfig({ slug: SLUG }));
  linkSidecar(dir, sidecarDir);

  console.error(`[ocel-e2e] reconciling the shared preview entry on ${wildcard} (from ${dir})`);
  const res = spawnSync(
    process.execPath,
    [
      join(adapterDir, "packages", "ocel", "bin", "run.js"),
      "domain",
      "use",
      wildcard,
      "--preview",
    ],
    {
      cwd: dir,
      stdio: ["ignore", "inherit", "inherit"],
      timeout: RECONCILE_TIMEOUT_MS,
      env: withoutSkipDriftChecks(process.env),
    },
  );

  if (res.error || res.signal || res.status !== 0) {
    const why =
      res.error?.message ??
      (res.signal ? `killed with ${res.signal}` : `exited with ${res.status}`);
    console.error(
      `[ocel-e2e] ENTRY RECONCILE FAILED for ${wildcard}: ${why}\n` +
        `[ocel-e2e] every preview this run deploys would serve through whichever entry ` +
        `worker was uploaded last, by whoever uploaded it — so the matrix would be ` +
        `measuring someone else's edge build, not this commit's.`,
    );
    return false;
  }
  console.error(`[ocel-e2e] shared preview entry reconciled on ${wildcard}`);
  return true;
}
