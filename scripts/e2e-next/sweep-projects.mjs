#!/usr/bin/env node
// Reclaims every e2e project an earlier run stranded, before this run mints its
// own. Driven unconditionally by the workflow's `sweep` job.
//
// This is the suite's ONLY orphan reclamation, and it is not merely a cost
// control. A run's project claims the preview wildcard domain by holding the
// worker route bound to it, so a project a cancelled or killed run left behind
// blocks EVERY future run, not just its own. Nothing else ever notices it: the
// per-fixture cleanup only removes pointers, and the `destroy` job only knows
// its own run's slug.
//
// Sweeping unconditionally — rather than only what looks abandoned — is safe
// because the workflow's concurrency group admits one run at a time, so any e2e
// project that is not this run's is by definition nobody's.
//
// A project it cannot reclaim fails the job. Continuing would deploy into a
// domain another project still claims, which fails later, slower and less
// legibly.

import { listParameterNames } from "./aws.mjs";
import { PREVIEW_ROOT_STACK_PARAM_PREFIX, projectSlugForRun, strandedProjectSlugs } from "./lib.mjs";
import { destroyProject } from "./project-teardown.mjs";

const keep = projectSlugForRun();
const stranded = strandedProjectSlugs(listParameterNames(PREVIEW_ROOT_STACK_PARAM_PREFIX), keep);

if (stranded.length === 0) {
  console.error(`[ocel-e2e] no stranded e2e projects; ${keep} is the only one`);
  process.exit(0);
}

console.error(`[ocel-e2e] ${stranded.length} stranded e2e project(s): ${stranded.join(", ")}`);

const failed = stranded.filter((slug) => !destroyProject(slug));
if (failed.length > 0) {
  console.error(`[ocel-e2e] could not reclaim ${failed.join(", ")} — they still hold the preview domain claim`);
  process.exit(1);
}

console.error(`[ocel-e2e] reclaimed ${stranded.length} stranded project(s)`);
