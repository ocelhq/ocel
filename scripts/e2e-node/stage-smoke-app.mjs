#!/usr/bin/env node

import { spawnSync } from "node:child_process";
import { cpSync, mkdtempSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));

const outFile = process.argv[2];

const dir = mkdtempSync(join(process.env.OCEL_E2E_NODE_STAGE_ROOT || tmpdir(), "node-e2e-"));
cpSync(resolve(here, "smoke-app"), dir, { recursive: true });

const res = spawnSync("npm", ["install", "--no-audit", "--no-fund", "--omit=dev"], {
  cwd: dir,
  stdio: ["ignore", "inherit", "inherit"],
});
if (res.error || res.status !== 0) {
  console.error(
    `[ocel-e2e-node] npm install in ${dir} failed: ${res.error?.message ?? `exited with ${res.status}`}`,
  );
  process.exit(1);
}

if (outFile) {
  writeFileSync(outFile, `${dir}\n`);
}
process.stdout.write(`${dir}\n`);
