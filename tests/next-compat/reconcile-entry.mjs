#!/usr/bin/env node

import { spawnSync } from "node:child_process";
import { mkdtempSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { pathToFileURL } from "node:url";

import {
  DEFAULT_COMPAT_TARGET,
  ENTRY_SLUG,
  ocelBinary,
  readCompatTarget,
  renderOcelConfig,
  requireNamespace,
  withoutSkipChecks,
} from "./lib.mjs";
import { linkSidecar } from "./sidecar.mjs";

const BOOTSTRAP_TIMEOUT_MS = 30 * 60 * 1000;
const RECONCILE_TIMEOUT_MS = 10 * 60 * 1000;
const KEY_WANT = "E2E_VARIABLES_KEY_WANTED";

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  const wildcard = process.argv[2] || process.env.OCEL_E2E_PREVIEW_DOMAIN;
  process.exit(reconcileEntry(wildcard) ? 0 : 1);
}

export function reconcileEntry(wildcard) {
  requireNamespace();
  const target = readCompatTarget();
  if (target.name !== DEFAULT_COMPAT_TARGET) {
    console.error(`[ocel-e2e] ${target.name} has no shared preview entry worker to reconcile`);
    return true;
  }
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
  writeFileSync(join(dir, "ocel.config.ts"), renderOcelConfig({ slug: ENTRY_SLUG }));
  linkSidecar(dir, sidecarDir);

  console.error(`[ocel-e2e] bootstrapping the preview tier and its variables key (from ${dir})`);
  const bootstrapFailure = bootstrapWithVariablesKey(adapterDir, dir);
  if (bootstrapFailure) {
    console.error(
      `[ocel-e2e] PREVIEW BOOTSTRAP FAILED: ${bootstrapFailure}\n` +
        `[ocel-e2e] nothing in this namespace can deploy, reconcile or destroy a preview without it.`,
    );
    return false;
  }

  console.error(`[ocel-e2e] reconciling the shared preview entry on ${wildcard} (from ${dir})`);
  const reconcileFailure = failureOf(
    adapterDir,
    dir,
    ["domain", "use", wildcard, "--preview"],
    RECONCILE_TIMEOUT_MS,
  );
  if (reconcileFailure) {
    console.error(
      `[ocel-e2e] ENTRY RECONCILE FAILED for ${wildcard}: ${reconcileFailure}\n` +
        `[ocel-e2e] every preview this run deploys would serve through whichever entry ` +
        `worker was uploaded last, by whoever uploaded it — so the matrix would be ` +
        `measuring someone else's edge build, not this commit's.`,
    );
    return false;
  }
  console.error(`[ocel-e2e] shared preview entry reconciled on ${wildcard}`);
  return true;
}

export function bootstrapWithVariablesKey(adapterDir, dir, run = attempt) {
  const bootstrap = () =>
    run(adapterDir, dir, ["bootstrap", "preview", "--yes"], BOOTSTRAP_TIMEOUT_MS);
  const first = bootstrap();
  if (first.failure) {
    return first.failure;
  }
  const wanted = run(
    adapterDir,
    dir,
    ["env", "set", `${KEY_WANT}=wanted`, "--preview"],
    RECONCILE_TIMEOUT_MS,
  );
  if (!wanted.failure) {
    return run(adapterDir, dir, ["env", "rm", KEY_WANT, "--preview"], RECONCILE_TIMEOUT_MS).failure;
  }
  if (!wanted.said.includes("variables-key")) {
    return wanted.failure;
  }
  return bootstrap().failure;
}

function attempt(adapterDir, dir, args, timeout) {
  const res = spawnSync(process.execPath, [ocelBinary(adapterDir), ...args], {
    cwd: dir,
    encoding: "utf8",
    stdio: ["ignore", "pipe", "pipe"],
    timeout,
    env: withoutSkipChecks(process.env),
  });
  process.stderr.write(res.stdout ?? "");
  process.stderr.write(res.stderr ?? "");
  return { failure: failureFrom(res), said: `${res.stdout ?? ""}${res.stderr ?? ""}` };
}

function failureOf(adapterDir, dir, args, timeout) {
  const res = spawnSync(process.execPath, [ocelBinary(adapterDir), ...args], {
    cwd: dir,
    stdio: ["ignore", "inherit", "inherit"],
    timeout,
    env: withoutSkipChecks(process.env),
  });
  return failureFrom(res);
}

function failureFrom(res) {
  if (res.error || res.signal || res.status !== 0) {
    return (
      res.error?.message ?? (res.signal ? `killed with ${res.signal}` : `exited with ${res.status}`)
    );
  }
  return "";
}
