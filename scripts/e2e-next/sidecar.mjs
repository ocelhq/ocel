// Pointing a directory at the prebuilt Ocel packages, which is the only thing
// any directory this suite creates ever sees of Ocel.
//
// Shared by deploy.mjs (the temp app it builds and deploys) and
// project-teardown.mjs (the scratch directory it addresses the project from):
// both write an ocel.config.ts that imports `ocel/config`, and both hand it to
// a CLI that bundles and executes it from that directory.

import { existsSync, lstatSync, mkdirSync, rmSync, symlinkSync } from "node:fs";
import { join } from "node:path";

/**
 * linkSidecar symlinks `ocel` and the `@ocel` scope into dir's node_modules.
 * Only those two: a caller may own the rest of node_modules (the harness's temp
 * app carries its own isolated `next`), and replacing the directory would break
 * the app it built.
 *
 * `ocel` (the bundled ocel.config.ts imports ocel/config) and
 * `@ocel/provider-aws-<platform>` (providerlocator require.resolve's it from
 * the project dir) are the two that must resolve from here.
 */
export function linkSidecar(dir, sidecarDir) {
  const modules = join(dir, "node_modules");
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
