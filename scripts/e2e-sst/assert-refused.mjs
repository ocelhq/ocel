#!/usr/bin/env node

import { spawnSync } from "node:child_process";
import { readFileSync, writeFileSync } from "node:fs";
import { join } from "node:path";

import { awsUnreachable, partitionRows, varsTable } from "./aws.mjs";
import {
  CLASS,
  CONSUMER_STATE_FILE,
  CUSTOM_LINK_NAME,
  LOG_PREFIX,
  linkPartitionKey,
  refusalProblem,
  renderOcelConfig,
} from "./lib.mjs";

const appDir = process.cwd();

const adapterDir = process.env.ADAPTER_DIR;
if (!adapterDir) {
  console.error(`${LOG_PREFIX} ADAPTER_DIR is not set; it must point at this repository`);
  process.exit(2);
}

const state = JSON.parse(readFileSync(join(appDir, CONSUMER_STATE_FILE), "utf8"));

const unreachable = awsUnreachable();
if (unreachable) {
  console.error(`${LOG_PREFIX} refusal: could not judge it, AWS is unreachable: ${unreachable}`);
  process.exit(2);
}

const table = varsTable();
if (!table) {
  console.error(`${LOG_PREFIX} refusal: could not judge it, this account holds no ocel bootstrap`);
  process.exit(2);
}

const surviving = partitionRows(table, linkPartitionKey(state.slug, CLASS, CUSTOM_LINK_NAME));
if (Object.keys(surviving).length > 0) {
  console.error(
    `${LOG_PREFIX} refusal: could not judge it, ${CUSTOM_LINK_NAME} is still published; this leg runs after destroy.mjs has taken the publisher down`,
  );
  process.exit(2);
}

const configPath = join(appDir, "ocel.config.ts");
const bound = renderOcelConfig({ slug: state.slug, host: state.host });
writeFileSync(configPath, renderOcelConfig({ slug: state.slug, host: state.host, links: [] }));

const built = ocel(["build"], "inherit");
if (built.status !== 0) {
  writeFileSync(configPath, bound);
  console.error(`${LOG_PREFIX} refusal: ocel build exited ${built.status}; the deploy never reached its transforms`);
  process.exit(1);
}

const deployed = ocel(["deploy", "--prebuilt", "--yes"], "pipe");
const output = `${deployed.stdout ?? ""}${deployed.stderr ?? ""}`;
process.stderr.write(output);
writeFileSync(configPath, bound);

const problem = refusalProblem(deployed.status, output);
if (problem) {
  console.error(`${LOG_PREFIX} refusal: FAILED — ${problem}`);
  if (deployed.status === 0) {
    spawnSync(
      process.execPath,
      [join(adapterDir, "packages", "ocel", "bin", "run.js"), "destroy", "production"],
      {
        cwd: appDir,
        stdio: "inherit",
        env: { ...process.env, OCEL_DESTROY_BYPASS_CONFIRMATION: state.slug },
      },
    );
  }
  process.exit(1);
}

console.error(
  `${LOG_PREFIX} refusal: PASSED — with ${CUSTOM_LINK_NAME} unpublished the deploy stopped at the transform that reads it`,
);

function ocel(args, stdio) {
  const res = spawnSync(
    process.execPath,
    [join(adapterDir, "packages", "ocel", "bin", "run.js"), ...args],
    {
      cwd: appDir,
      stdio,
      encoding: "utf8",
      timeout: Number(process.env.OCEL_E2E_SST_CONSUME_TIMEOUT_MS || 30 * 60 * 1000),
    },
  );
  if (res.error) {
    console.error(`${LOG_PREFIX} refusal: ocel ${args[0]}: ${res.error.message}`);
    process.exit(1);
  }
  return res;
}
