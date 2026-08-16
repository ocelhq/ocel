#!/usr/bin/env node

import { spawnSync } from "node:child_process";
import { readFileSync } from "node:fs";
import { join } from "node:path";

import { awsUnreachable, partitionRows, varsTable } from "./aws.mjs";
import { CLASS, LINK_NAME, LOG_PREFIX, STATE_FILE, linkPartitionKey, refuseUntilRePlumbed } from "./lib.mjs";

refuseUntilRePlumbed();

const stage = process.env.OCEL_E2E_SST_STAGE || "e2e";

const removed = spawnSync("npx", ["sst", "remove", "--stage", stage], {
  cwd: process.cwd(),
  stdio: "inherit",
  timeout: Number(process.env.OCEL_E2E_SST_TIMEOUT_MS || 45 * 60 * 1000),
});
if (removed.status !== 0) {
  console.error(`${LOG_PREFIX} sst remove exited ${removed.status}; the published records may still be live`);
  process.exit(1);
}

const unreachable = awsUnreachable();
if (unreachable) {
  console.error(`${LOG_PREFIX} destroy: could not judge the prune, AWS is unreachable: ${unreachable}`);
  process.exit(2);
}

const state = JSON.parse(readFileSync(join(process.cwd(), STATE_FILE), "utf8"));
const table = varsTable();
if (!table) {
  console.error(`${LOG_PREFIX} destroy: could not judge the prune, this account holds no ocel bootstrap`);
  process.exit(2);
}

const left = partitionRows(table, linkPartitionKey(state.project, CLASS, LINK_NAME));
if (Object.keys(left).length > 0) {
  console.error(`${LOG_PREFIX} destroy: FAILED — ${Object.keys(left).join(", ")} survived the destroy as consumable state`);
  process.exit(1);
}

console.error(`${LOG_PREFIX} destroy: PASSED — the destroy took the published record with it`);
