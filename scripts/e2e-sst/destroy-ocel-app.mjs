#!/usr/bin/env node

import { spawnSync } from "node:child_process";
import { readFileSync } from "node:fs";
import { join } from "node:path";

import { awsUnreachable, partitionRows, varsTable } from "./aws.mjs";
import {
  CLASS,
  CONSUMER_STATE_FILE,
  LINK_NAME,
  LOG_PREFIX,
  linkPartitionKey,
  recordSortKey,
} from "./lib.mjs";

const appDir = process.cwd();

const adapterDir = process.env.ADAPTER_DIR;
if (!adapterDir) {
  console.error(`${LOG_PREFIX} ADAPTER_DIR is not set; it must point at this repository`);
  process.exit(2);
}

const state = JSON.parse(readFileSync(join(appDir, CONSUMER_STATE_FILE), "utf8"));

const destroyed = spawnSync(
  process.execPath,
  [join(adapterDir, "packages", "ocel", "bin", "run.js"), "destroy", "production"],
  {
    cwd: appDir,
    stdio: "inherit",
    env: { ...process.env, OCEL_DESTROY_BYPASS_CONFIRMATION: state.slug },
    timeout: Number(process.env.OCEL_E2E_SST_CONSUME_TIMEOUT_MS || 30 * 60 * 1000),
  },
);
if (destroyed.status !== 0) {
  console.error(`${LOG_PREFIX} ocel destroy production exited ${destroyed.status}; the consuming project may still be live`);
  process.exit(1);
}

const unreachable = awsUnreachable();
if (unreachable) {
  console.error(`${LOG_PREFIX} consumer teardown: could not judge it, AWS is unreachable: ${unreachable}`);
  process.exit(2);
}

const table = varsTable();
if (!table) {
  console.error(`${LOG_PREFIX} consumer teardown: could not judge it, this account holds no ocel bootstrap`);
  process.exit(2);
}

const rows = partitionRows(table, linkPartitionKey(state.slug, CLASS, LINK_NAME));
if (!rows[recordSortKey("")]) {
  console.error(`${LOG_PREFIX} consumer teardown: FAILED — tearing the consuming project down took the publisher's record with it`);
  process.exit(1);
}

console.error(`${LOG_PREFIX} consumer teardown: PASSED — the published record outlived the app that consumed it`);
