#!/usr/bin/env node

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
if (existsSync(join(src, "proxy.ts"))) {
  cpSync(join(src, "proxy.ts"), join(installDir, "proxy.ts"));
}
writeFileSync(outFile, installDir);

function mockTrace() {
  return {
    traceAsyncFn: (fn) => fn(mockTrace()),
    traceFn: (fn) => fn(mockTrace()),
    traceChild: () => mockTrace(),
  };
}
