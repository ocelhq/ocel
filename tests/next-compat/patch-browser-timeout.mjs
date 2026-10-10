#!/usr/bin/env node

import { readFileSync, writeFileSync } from "node:fs";
import { join } from "node:path";

import { BROWSER_TIMEOUT_ENV, withBrowserTimeoutEnv } from "./lib.mjs";

const HARNESS_BROWSER = join("test", "lib", "browsers", "playwright.ts");

const [nextjsDir] = process.argv.slice(2);
if (!nextjsDir) {
  console.error("usage: patch-browser-timeout.mjs <nextjs-dir>");
  process.exit(2);
}

const path = join(nextjsDir, HARNESS_BROWSER);
try {
  writeFileSync(path, withBrowserTimeoutEnv(readFileSync(path, "utf8")));
} catch (err) {
  console.error(`[ocel-e2e] could not patch ${path}: ${err.message}`);
  process.exit(1);
}
console.error(`[ocel-e2e] ${path} takes Playwright's default timeout from ${BROWSER_TIMEOUT_ENV}`);
