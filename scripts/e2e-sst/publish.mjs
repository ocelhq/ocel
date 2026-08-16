#!/usr/bin/env node

import { spawnSync } from "node:child_process";
import { readFileSync, writeFileSync } from "node:fs";
import { join } from "node:path";

import { LOG_PREFIX, STATE_FILE, parseSstOutputs } from "./lib.mjs";

const cwd = process.cwd();
const stage = process.env.OCEL_E2E_SST_STAGE || "e2e";
const timeout = Number(process.env.OCEL_E2E_SST_TIMEOUT_MS || 45 * 60 * 1000);

const result = spawnSync("npx", ["sst", "deploy", "--stage", stage], {
  cwd,
  stdio: "inherit",
  timeout,
});

if (result.error) {
  console.error(`${LOG_PREFIX} sst deploy could not start: ${result.error.message}`);
  process.exit(2);
}
if (result.status !== 0) {
  console.error(`${LOG_PREFIX} sst deploy exited ${result.status}`);
  process.exit(1);
}
console.error(`${LOG_PREFIX} sst deploy landed; the link resource published as part of it`);

const shown = spawnSync("npx", ["sst", "outputs", "--stage", stage], {
  cwd,
  encoding: "utf8",
  timeout,
});
if (shown.status !== 0) {
  console.error(`${LOG_PREFIX} sst outputs exited ${shown.status}; the consume leg has nothing to check against`);
  process.exit(1);
}

const outputs = parseSstOutputs(shown.stdout);
for (const key of ["host", "port", "database", "subnetIds", "securityGroupIds"]) {
  if (!outputs[key]) {
    console.error(`${LOG_PREFIX} sst published no ${key} output; the consume leg has nothing to check against`);
    process.exit(1);
  }
}

const statePath = join(cwd, STATE_FILE);
const state = JSON.parse(readFileSync(statePath, "utf8"));
writeFileSync(statePath, JSON.stringify({ ...state, stage, outputs }, null, 2));
console.error(`${LOG_PREFIX} the published resource answers at ${outputs.host}`);
