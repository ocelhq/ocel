#!/usr/bin/env node

import { spawnSync } from "node:child_process";
import { existsSync, readFileSync } from "node:fs";
import { join } from "node:path";

import { CONSUMER_STATE_FILE, DEPLOY_RESULT_FILE, LOG_PREFIX, refuseUntilRePlumbed } from "./lib.mjs";

refuseUntilRePlumbed();

const appDir = process.cwd();

const adapterDir = process.env.ADAPTER_DIR;
if (!adapterDir) {
  console.error(`${LOG_PREFIX} ADAPTER_DIR is not set; it must point at this repository`);
  process.exit(2);
}

const state = JSON.parse(readFileSync(join(appDir, CONSUMER_STATE_FILE), "utf8"));

const deadline =
  Date.now() + (Number(process.env.OCEL_E2E_SST_CONSUME_TIMEOUT_MS) || 30 * 60 * 1000);

run("ocel build", ["build"]);
run("ocel deploy", ["deploy", "--prebuilt", "--yes"]);

const resultPath = join(appDir, DEPLOY_RESULT_FILE);
if (!existsSync(resultPath)) {
  console.error(`${LOG_PREFIX} ${resultPath} was not written; the deploy produced no result to judge`);
  process.exit(1);
}
const result = JSON.parse(readFileSync(resultPath, "utf8"));
if (result.slug !== state.slug) {
  console.error(`${LOG_PREFIX} the deploy landed under ${result.slug}, want ${state.slug}`);
  process.exit(1);
}

console.error(`${LOG_PREFIX} the consuming app deployed; it binds ${state.link} from the publisher's record`);
for (const url of result.appUrls ?? []) {
  console.log(url);
}

function run(label, args) {
  const remaining = deadline - Date.now();
  if (remaining <= 0) {
    console.error(`${LOG_PREFIX} the consume budget was exhausted before ${label} started`);
    process.exit(1);
  }
  const res = spawnSync(
    process.execPath,
    [join(adapterDir, "packages", "ocel", "bin", "run.js"), ...args],
    { cwd: appDir, stdio: "inherit", timeout: remaining, killSignal: "SIGTERM" },
  );
  if (res.error) {
    console.error(`${LOG_PREFIX} ${label}: ${res.error.message}`);
    process.exit(1);
  }
  if (res.status !== 0) {
    console.error(`${LOG_PREFIX} ${label} exited ${res.status}`);
    process.exit(1);
  }
}
