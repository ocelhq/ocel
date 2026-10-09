#!/usr/bin/env node

import { POLL_INTERVAL_MS, previewStateTable, queryStatePartition, sleep } from "./aws.mjs";
import { listProjectSlugs, readAccessToken } from "./gcp.mjs";
import {
  DEPLOYED_PARTITIONS,
  projectSlugForRun,
  readCompatTarget,
  requireNamespace,
  retryDelayMs,
  selectStrandedAppSlugs,
  strandedProjectSlugs,
} from "./lib.mjs";
import { destroyProject } from "./project-teardown.mjs";

const LIST_DEADLINE_MS = 120_000;

requireNamespace();

const target = readCompatTarget();

async function listDeployedProjects() {
  const deadline = Date.now() + LIST_DEADLINE_MS;
  for (;;) {
    try {
      const table = previewStateTable();
      return table ? DEPLOYED_PARTITIONS.flatMap((pk) => queryStatePartition(table, pk)) : [];
    } catch (err) {
      if (Date.now() >= deadline) {
        console.error(
          `[ocel-e2e] could not read the preview state table at all within ` +
            `${LIST_DEADLINE_MS / 1000}s — every attempt failed, so nothing here says which projects are ` +
            `stranded: ${err.stderr || err.message}`,
        );
        process.exit(1);
      }
      console.error(
        `[ocel-e2e] could not read the preview state table (${err.stderr || err.message}); will retry`,
      );
      await sleep(POLL_INTERVAL_MS);
    }
  }
}

async function listGcpProjectSlugs() {
  const deadline = Date.now() + LIST_DEADLINE_MS;
  for (let attempt = 0; ; attempt++) {
    try {
      return await listProjectSlugs({
        ...target.gcp,
        namespace: process.env.OCEL_NAMESPACE,
        token: readAccessToken(),
      });
    } catch (err) {
      if (Date.now() >= deadline) {
        console.error(
          `[ocel-e2e] could not list the Cloud Run services of ${target.gcp.project} at all within ` +
            `${LIST_DEADLINE_MS / 1000}s, so nothing here says which projects are stranded: ${err.message}`,
        );
        process.exit(1);
      }
      console.error(
        `[ocel-e2e] could not list the Cloud Run services (${err.message}); will retry`,
      );
      await sleep(retryDelayMs(attempt));
    }
  }
}

const keep = projectSlugForRun();
const stranded =
  target.name === "aws-cloudflare"
    ? strandedProjectSlugs(await listDeployedProjects(), keep)
    : selectStrandedAppSlugs(await listGcpProjectSlugs(), keep);

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
