// linkSidecar points a deployed app at the prebuilt Ocel packages, shared by
// every harness's deploy.mjs (scripts/e2e-next, scripts/e2e-node). Not in
// lib.mjs: it does real filesystem work rather than compute a value, so it
// has no unit test and stays out of the module that promises one.
//
// Only `ocel` and the @ocel scope are linked: a harness's own package manager
// owns the rest of node_modules (a Next app's isolated `next`, an express
// app's `express`), and replacing the directory would break what it installed.
//
// `ocel` (a rendered ocel.config.ts imports ocel/config) and
// `@ocel/provider-aws-<platform>` (providerlocator require.resolve's it from
// the project dir) are the two that must resolve from the app directory.

import { existsSync, lstatSync, mkdirSync, rmSync, symlinkSync } from "node:fs";
import { join } from "node:path";

export function linkSidecar(appDir, sidecarDir) {
  const modules = join(appDir, "node_modules");
  mkdirSync(modules, { recursive: true });
  for (const name of ["ocel", "@ocel"]) {
    const target = join(sidecarDir, "node_modules", name);
    if (!existsSync(target)) {
      throw new Error(
        `sidecar has no ${name} package at ${target}. A sidecar packed before ` +
          `@ocel/sdk folded into the root ocel package carries only @ocel/*; ` +
          `repack it from the ocel and @ocel/provider-aws* tarballs — see ` +
          `"Repacking the sidecar" in scripts/e2e-next/README.md.`,
      );
    }
    const link = join(modules, name);
    if (existsSync(link) || isSymlink(link)) {
      rmSync(link, { recursive: true, force: true });
    }
    symlinkSync(target, link, "dir");
  }
}

function isSymlink(path) {
  try {
    return lstatSync(path).isSymbolicLink();
  } catch {
    return false;
  }
}
