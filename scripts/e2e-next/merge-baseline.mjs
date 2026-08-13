#!/usr/bin/env node

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
  console.error(
    `[ocel-e2e] merged ${fragments.length} fragments into ${out} (${Object.keys(merged.suites).length} suites)`,
  );
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
