#!/usr/bin/env node
// Builds the known-failure baseline (NEXT_EXTERNAL_TESTS_FILTERS) the matrix
// runs against, out of a recording run's per-suite Jest results.
//
// Two modes, matching the two halves of the workflow:
//   collect <nextjs-dir> <out.json>   one group, in the nextjs checkout: reduce
//                                     every *.results.json run-tests.js wrote
//                                     into a manifest fragment
//   merge <out.json> <fragment...>    the baseline job: fold every group's
//                                     fragment into the manifest to commit
//
// The manifest is the harness's unversioned shape — { "<test file>": { passed,
// failed, flakey, runtimeError } } — where a listed suite's `failed` cases are
// excluded from the run and a newly added case is included automatically. So
// promoting a fix is a matter of deleting its line, not re-recording.

import { readFileSync, readdirSync, statSync, writeFileSync } from "node:fs";
import { join, relative } from "node:path";

import { buildBaselineManifest, mergeBaselineManifest } from "./lib.mjs";

const [mode, ...args] = process.argv.slice(2);

if (mode === "collect") {
  const [nextjsDir, out] = args;
  if (!nextjsDir || !out) {
    usage();
  }
  const files = resultsFiles(join(nextjsDir, "test")).map((path) => ({
    path: relative(nextjsDir, path),
    results: JSON.parse(readFileSync(path, "utf8")),
  }));
  write(out, buildBaselineManifest(files));
  console.error(`[ocel-e2e] collected ${files.length} suite results into ${out}`);
} else if (mode === "merge") {
  const [out, ...fragments] = args;
  if (!out || fragments.length === 0) {
    usage();
  }
  const merged = mergeBaselineManifest(fragments.map((path) => JSON.parse(readFileSync(path, "utf8"))));
  write(out, merged);
  console.error(`[ocel-e2e] merged ${fragments.length} fragments into ${out} (${Object.keys(merged).length} suites)`);
} else {
  usage();
}

function resultsFiles(dir) {
  const found = [];
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    const path = join(dir, entry.name);
    if (entry.isDirectory()) {
      found.push(...resultsFiles(path));
    } else if (entry.isFile() && entry.name.endsWith(".results.json") && statSync(path).size > 0) {
      found.push(path);
    }
  }
  return found;
}

function write(path, manifest) {
  writeFileSync(path, JSON.stringify(manifest, null, 2) + "\n");
}

function usage() {
  console.error(
    "usage: merge-baseline.mjs collect <nextjs-dir> <out.json>\n" +
      "       merge-baseline.mjs merge <out.json> <fragment.json...>",
  );
  process.exit(2);
}
