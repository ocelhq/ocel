#!/usr/bin/env node

import { setTimeout as sleep } from "node:timers/promises";

import { previewBootstrapped } from "./aws.mjs";
import {
  projectSlugForRun,
  readCompatTarget,
  requireNamespace,
  retryDelayMs,
  selectStrandedAppSlugs,
  strandedProjectSlugs,
} from "./lib.mjs";
import { destroyProject, listProjects } from "./project-teardown.mjs";

const LIST_DEADLINE_MS = 120_000;

requireNamespace();

const target = readCompatTarget();

async function listHeldProjects() {
  const deadline = Date.now() + LIST_DEADLINE_MS;
  for (let attempt = 0; ; attempt++) {
    try {
      if (target.name === "aws-cloudflare" && !previewBootstrapped()) {
        return [];
      }
      return listProjects(target);
    } catch (err) {
      if (Date.now() >= deadline) {
        console.error(
          `[ocel-e2e] could not list the projects ocel holds at all within ` +
            `${LIST_DEADLINE_MS / 1000}s, so nothing here says which projects are stranded: ` +
            `${err.stderr || err.message}`,
        );
        process.exit(1);
      }
      console.error(
        `[ocel-e2e] could not list the projects ocel holds (${err.stderr || err.message}); will retry`,
      );
      await sleep(retryDelayMs(attempt));
    }
  }
}

const keep = projectSlugForRun();
const held = await listHeldProjects();
const stranded =
  target.name === "aws-cloudflare"
    ? strandedProjectSlugs(held, keep)
    : selectStrandedAppSlugs(held, keep);

if (stranded.length === 0) {
  console.error(`[ocel-e2e] no stranded e2e projects; ${keep} is the only one`);
  process.exit(0);
}

console.error(`[ocel-e2e] ${stranded.length} stranded e2e project(s): ${stranded.join(", ")}`);

const failed = [];
for (const slug of stranded) {
  if (!(await destroyProject(slug, target))) {
    failed.push(slug);
  }
}
if (failed.length > 0) {
  console.error(
    `[ocel-e2e] could not reclaim ${failed.join(", ")} — their preview footprint keeps billing`,
  );
  process.exit(1);
}

console.error(`[ocel-e2e] reclaimed ${stranded.length} stranded project(s)`);
