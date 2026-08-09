#!/usr/bin/env node
// Builds the known-failure baseline (NEXT_EXTERNAL_TESTS_FILTERS) the matrix
// runs against, out of a recording run's per-suite Jest results.
//
// Two modes, matching the two halves of the workflow:
//   collect <nextjs-dir> <log> <out.json>  one group: reduce every suite result
//                                          run-tests.js framed in its stdout into
//                                          a manifest fragment
//   merge <out.json> <fragment...>         the baseline job: fold every group's
//                                          fragment into the manifest to commit
//
// collect reads the harness's stdout, not the `.results.json` files it leaves
// behind: a failing top-level suite makes the harness `git clean -fdx` the whole
// of test/e2e between retries, which deletes the other 51 suites' results. It
// then fails loudly if any suite the harness started has no result, so a group
// can never again record a fraction of its run and exit 0.
//
// The manifest is the harness's unversioned shape — { "<test file>": { failed,
// flakey, runtimeError } } — where a listed suite's `failed` cases are excluded
// from the run and a newly added case is included automatically. So promoting a
// fix is a matter of deleting its line, not re-recording.
//
// Those three fields are all test/get-test-filter.js reads, and only suites with
// something to exclude are listed: an unlisted suite runs in full. The manifest
// is therefore a list of outstanding work, and empties as the adapter is fixed.

import { readFileSync, writeFileSync } from "node:fs";
import { resolve } from "node:path";

import {
  buildBaselineManifest,
  mergeBaselineManifest,
  suitesFromHarnessOutput,
  suitesStartedInHarnessOutput,
} from "./lib.mjs";

const [mode, ...args] = process.argv.slice(2);

if (mode === "collect") {
  const [nextjsDir, log, out] = args;
  if (!nextjsDir || !log || !out) {
    usage();
  }
  const stdout = readFileSync(log, "utf8");
  const suites = suitesFromHarnessOutput(stdout, resolve(nextjsDir));
  const missing = suitesStartedInHarnessOutput(stdout).filter(
    (suite) => !suites.some((collected) => collected.suite === suite),
  );
  if (missing.length > 0) {
    console.error(
      `[ocel-e2e] ${missing.length} suite(s) the harness started produced no result in ${log}; ` +
        `the fragment would be incomplete:\n  ${missing.join("\n  ")}`,
    );
    process.exit(1);
  }
  write(out, buildBaselineManifest(suites));
  console.error(`[ocel-e2e] collected ${suites.length} suite results into ${out}`);
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

function write(path, manifest) {
  writeFileSync(path, JSON.stringify(manifest, null, 2) + "\n");
}

function usage() {
  console.error(
    "usage: merge-baseline.mjs collect <nextjs-dir> <harness-log> <out.json>\n" +
      "       merge-baseline.mjs merge <out.json> <fragment.json...>",
  );
  process.exit(2);
}
