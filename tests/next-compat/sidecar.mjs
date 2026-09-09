import { existsSync, lstatSync, mkdirSync, rmSync, symlinkSync } from "node:fs";
import { join } from "node:path";

export function linkSidecar(dir, sidecarDir) {
  const modules = join(dir, "node_modules");
  mkdirSync(modules, { recursive: true });
  for (const name of ["ocel", "@ocel"]) {
    const target = join(sidecarDir, "node_modules", name);
    if (!existsSync(target)) {
      throw new Error(
        `sidecar has no ${name} package at ${target}. Repack it from the ocel ` +
          `tarball — see "Repacking the sidecar" in tests/next-compat/README.md.`,
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
