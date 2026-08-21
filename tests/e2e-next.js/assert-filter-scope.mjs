#!/usr/bin/env node

import { readdirSync, statSync } from "node:fs";
import { createRequire } from "node:module";
import { join, relative, resolve } from "node:path";
import { pathToFileURL } from "node:url";

const UPSTREAM_MANIFEST = "test/deploy-tests-manifest.json";

export function assertFilterScope(nextjsDir, filters) {
  const root = resolve(nextjsDir);
  const require = createRequire(join(root, "package.json"));
  const { mergeManifests, getTestFilterFromManifest } = require("./test/get-test-filter.js");
  const paths = filters.split(",").filter(Boolean);
  const problems = [];

  if (!paths.includes(UPSTREAM_MANIFEST)) {
    problems.push(
      `${UPSTREAM_MANIFEST} is not in the chain (got ${paths.join(",") || "nothing"}).\n  ` +
        `Next's own deploy manifest is what puts 28 suites out of adapter scope and names the ` +
        `cases that fail for every adapter. Without it they all run against Ocel with no entry ` +
        `explaining them.`,
    );
  }

  const files = [];
  for (const entry of walk(join(root, "test", "e2e"))) {
    if (/\.test\.[jt]sx?$/.test(entry)) files.push(relative(root, entry));
  }

  const merged = mergeManifests(paths.map((path) => structuredClone(require(resolve(root, path)))));
  const scheduled = new Set(
    getTestFilterFromManifest(merged)(files.map((file) => ({ file }))).map((test) => test.file),
  );
  const upstream = require(resolve(root, UPSTREAM_MANIFEST));
  const stillScheduled = (upstream.rules?.exclude ?? [])
    .filter((pattern) => !pattern.includes("*"))
    .filter((suite) => scheduled.has(suite));
  if (stillScheduled.length > 0) {
    problems.push(
      `${stillScheduled.length} suite(s) upstream puts out of deploy-adapter scope are still ` +
        `scheduled:\n  ` + stillScheduled.join("\n  "),
    );
  }

  const ours = paths.filter((path) => path !== UPSTREAM_MANIFEST).map((path) => require(resolve(root, path)));
  const deadWeight = ours
    .flatMap((manifest) => Object.keys(manifest.suites ?? {}))
    .filter((suite) => files.includes(suite) && !scheduled.has(suite));
  if (deadWeight.length > 0) {
    problems.push(
      `${deadWeight.length} suite(s) our baseline lists never run, so their recorded cases are ` +
        `dead weight:\n  ` + deadWeight.join("\n  "),
    );
  }

  if (problems.length > 0) {
    throw new Error(problems.join("\n\n"));
  }

  return `${scheduled.size} of ${files.length} e2e suites scheduled, ` +
    `${files.length - scheduled.size} excluded across ${paths.length} chained manifest(s)`;
}

if (process.argv[1] && pathToFileURL(resolve(process.argv[1])).href === import.meta.url) {
  const [nextjsDir, filters] = process.argv.slice(2);
  if (!nextjsDir || !filters) {
    console.error(
      "usage: assert-filter-scope.mjs <nextjs-dir> <NEXT_EXTERNAL_TESTS_FILTERS>\n" +
        "       pass the env var verbatim, comma separated, resolved from <nextjs-dir>",
    );
    process.exit(2);
  }
  try {
    console.error(`[ocel-e2e] filter scope holds: ${assertFilterScope(nextjsDir, filters)}`);
  } catch (error) {
    console.error("[ocel-e2e] filter scope is wrong:\n");
    console.error(error.message);
    process.exit(1);
  }
}

function* walk(dir) {
  let entries;
  try {
    entries = readdirSync(dir);
  } catch {
    return;
  }
  for (const name of entries) {
    const full = join(dir, name);
    if (statSync(full).isDirectory()) yield* walk(full);
    else yield full;
  }
}
