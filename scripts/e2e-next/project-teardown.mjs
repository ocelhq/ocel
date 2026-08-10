#!/usr/bin/env node
// Tears down one whole e2e PROJECT: every preview pointer left in it, its
// per-name infra stacks, its deployments-store instance, its preview entrypoint
// worker and the wildcard route that worker holds.
//
//   node project-teardown.mjs [slug]      # defaults to this run's project
//
// Driven twice by the workflow: by the `destroy` job for the run's own project,
// and by sweep-projects.mjs for each project an earlier run stranded. That
// second caller is why this addresses a project by slug rather than by
// directory — the project being reclaimed belongs to no checkout on this
// runner, and nothing of it survives but its slug.
//
// `ocel destroy --preview` resolves the project through the ocel.config.ts in
// its working directory, so this renders a minimal one (slug + provider, and
// the preview wildcard the project declared) into a scratch directory and runs
// there. Rendering rather than reusing a deployed app's config is the point: a
// stranded project has no app directory left to run from.

import { spawnSync } from "node:child_process";
import { mkdtempSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { pathToFileURL } from "node:url";

import { projectSlugForRun, renderOcelConfig } from "./lib.mjs";
import { linkSidecar } from "./sidecar.mjs";

const TEARDOWN_TIMEOUT_MS = 30 * 60 * 1000;

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  const slug = process.argv[2] || projectSlugForRun();
  process.exit(destroyProject(slug) ? 0 : 1);
}

/**
 * destroyProject drives `ocel destroy --preview --yes` for one project and
 * reports whether it succeeded, rather than exiting: the sweeper drives several
 * in a row and one stranded project it cannot reclaim must not stop it trying
 * the rest.
 */
export function destroyProject(slug) {
  const adapterDir = process.env.ADAPTER_DIR;
  const sidecarDir = process.env.OCEL_E2E_SIDECAR_DIR;
  if (!adapterDir || !sidecarDir) {
    throw new Error("project-teardown needs ADAPTER_DIR and OCEL_E2E_SIDECAR_DIR");
  }

  const dir = mkdtempSync(join(tmpdir(), `ocel-e2e-teardown-${slug}-`));
  writeFileSync(
    join(dir, "ocel.config.ts"),
    renderOcelConfig({ slug, previewDomain: process.env.OCEL_E2E_PREVIEW_DOMAIN || "" }),
  );
  linkSidecar(dir, sidecarDir);

  console.error(`[ocel-e2e] destroying the preview footprint of project ${slug} (from ${dir})`);
  const res = spawnSync(
    process.execPath,
    [join(adapterDir, "packages", "ocel", "bin", "run.js"), "destroy", "--preview", "--yes"],
    { cwd: dir, stdio: ["ignore", "inherit", "inherit"], timeout: TEARDOWN_TIMEOUT_MS },
  );

  if (res.error || res.signal || res.status !== 0) {
    const why = res.error?.message ?? (res.signal ? `killed with ${res.signal}` : `exited with ${res.status}`);
    console.error(
      `[ocel-e2e] PROJECT TEARDOWN FAILED for ${slug}: ${why}\n` +
        `[ocel-e2e] it still holds the preview domain's wildcard route, which is an ` +
        `account-wide claim: until it is reclaimed no other project can deploy previews ` +
        `onto that domain. Retry with \`node scripts/e2e-next/project-teardown.mjs ${slug}\`.`,
    );
    return false;
  }
  console.error(`[ocel-e2e] project ${slug} destroyed`);
  return true;
}
