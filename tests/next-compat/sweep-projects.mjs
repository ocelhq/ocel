#!/usr/bin/env node

import { POLL_INTERVAL_MS, listParameterNames, sleep } from "./aws.mjs";
import { PREVIEW_ROOT_STACK_PARAM_PREFIX, projectSlugForRun, strandedProjectSlugs } from "./lib.mjs";
import { destroyProject } from "./project-teardown.mjs";

const LIST_DEADLINE_MS = 120_000;

async function listRootStackParams() {
  const deadline = Date.now() + LIST_DEADLINE_MS;
  for (;;) {
    try {
      return listParameterNames(PREVIEW_ROOT_STACK_PARAM_PREFIX);
    } catch (err) {
      if (Date.now() >= deadline) {
        console.error(
          `[ocel-e2e] could not list ${PREVIEW_ROOT_STACK_PARAM_PREFIX} at all within ` +
            `${LIST_DEADLINE_MS / 1000}s — every attempt failed, so nothing here says which projects are ` +
            `stranded: ${err.message}`,
        );
        process.exit(1);
      }
      console.error(`[ocel-e2e] could not list ${PREVIEW_ROOT_STACK_PARAM_PREFIX} (${err.message}); will retry`);
      await sleep(POLL_INTERVAL_MS);
    }
  }
}

const keep = projectSlugForRun();
const stranded = strandedProjectSlugs(await listRootStackParams(), keep);

if (stranded.length === 0) {
  console.error(`[ocel-e2e] no stranded e2e projects; ${keep} is the only one`);
  process.exit(0);
}

console.error(`[ocel-e2e] ${stranded.length} stranded e2e project(s): ${stranded.join(", ")}`);

const failed = stranded.filter((slug) => !destroyProject(slug));
if (failed.length > 0) {
  console.error(`[ocel-e2e] could not reclaim ${failed.join(", ")} — their preview footprint keeps billing`);
  process.exit(1);
}

console.error(`[ocel-e2e] reclaimed ${stranded.length} stranded project(s)`);
