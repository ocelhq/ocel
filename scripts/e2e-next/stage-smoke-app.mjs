#!/usr/bin/env node
// Stages the smoke app for the workflow's smoke job and prints its directory.
//
// The Next it builds against comes from the `nextjs` checkout, installed by the
// harness's own test/lib/create-next-install — the same code path that installs
// every temp app the matrix tests. A registry pin here would smoke-test a
// different Next than the matrix whenever the workflow is dispatched with a
// nextjsRef other than the default.
//
// Usage: stage-smoke-app.mjs <nextjs-dir> <smoke-app-src-dir>

import { cpSync, readFileSync } from "node:fs";
import { createRequire } from "node:module";
import { join, resolve } from "node:path";

const [nextjsDir, srcDir] = process.argv.slice(2);
if (!nextjsDir || !srcDir) {
  console.error("usage: stage-smoke-app.mjs <nextjs-dir> <smoke-app-src-dir>");
  process.exit(2);
}

const nextjs = resolve(nextjsDir);
const src = resolve(srcDir);
const require = createRequire(join(nextjs, "package.json"));
const { createNextInstall } = require("./test/lib/create-next-install");

const pkg = JSON.parse(readFileSync(join(src, "package.json"), "utf8"));
const { installDir } = await createNextInstall({
  parentSpan: mockTrace(),
  dependencies: pkg.dependencies ?? {},
  packageJson: { scripts: pkg.scripts },
});

cpSync(join(src, "app"), join(installDir, "app"), { recursive: true });
process.stdout.write(installDir + "\n");

// createNextInstall traces its own work; the harness passes a no-op span when it
// has no tracer to hand it, and so do we.
function mockTrace() {
  return {
    traceAsyncFn: (fn) => fn(mockTrace()),
    traceFn: (fn) => fn(mockTrace()),
    traceChild: () => mockTrace(),
  };
}
