#!/usr/bin/env node
// Stages the smoke app for the workflow's smoke job and writes its directory to
// <out-file>. Not stdout: createNextInstall logs to this process's stdout and
// spawns pnpm with stdout inherited, so the fd carries the harness's chatter and
// can't also carry a result.
//
// The Next it builds against comes from the `nextjs` checkout, installed by the
// harness's own test/lib/create-next-install — the same code path that installs
// every temp app the matrix tests. A registry pin here would smoke-test a
// different Next than the matrix whenever the workflow is dispatched with a
// nextjsRef other than the default.
//
// Usage: stage-smoke-app.mjs <nextjs-dir> <smoke-app-src-dir> <out-file>

import { cpSync, existsSync, readFileSync, writeFileSync } from "node:fs";
import { createRequire } from "node:module";
import { join, resolve } from "node:path";

const [nextjsDir, srcDir, outFile] = process.argv.slice(2);
if (!nextjsDir || !srcDir || !outFile) {
  console.error("usage: stage-smoke-app.mjs <nextjs-dir> <smoke-app-src-dir> <out-file>");
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
// proxy.ts (node middleware) lives at the project root beside app/, same as a
// real app — copied only when the fixture carries one, so a smoke app with
// none stays exactly as it was before this existed.
if (existsSync(join(src, "proxy.ts"))) {
  cpSync(join(src, "proxy.ts"), join(installDir, "proxy.ts"));
}
writeFileSync(outFile, installDir);

// createNextInstall traces its own work; the harness passes a no-op span when it
// has no tracer to hand it, and so do we.
function mockTrace() {
  return {
    traceAsyncFn: (fn) => fn(mockTrace()),
    traceFn: (fn) => fn(mockTrace()),
    traceChild: () => mockTrace(),
  };
}
