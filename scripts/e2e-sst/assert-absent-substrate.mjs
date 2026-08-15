#!/usr/bin/env node

import { spawnSync } from "node:child_process";
import { existsSync } from "node:fs";

import { LOG_PREFIX, PUBLISHER, namesAbsentSubstrate, projectSlugForRun, publisherBinary } from "./lib.mjs";

function die(message) {
  console.error(`${LOG_PREFIX} ${message}`);
  process.exit(2);
}

const region = process.env.OCEL_E2E_SST_EMPTY_REGION;
if (!region) {
  die("OCEL_E2E_SST_EMPTY_REGION is not set; this leg needs a region this account has never bootstrapped");
}

let binary;
try {
  binary = publisherBinary();
} catch (err) {
  die(`no ocel AWS publisher package for ${process.platform}-${process.arch}: ${err.message}`);
}
if (!existsSync(binary)) {
  die(`${binary} has not been built; run the provider build before this leg`);
}

const request = {
  project: projectSlugForRun(),
  publisher: PUBLISHER,
  class: "production",
  region,
  records: [
    {
      name: "orders",
      type: "sst:aws.Postgres",
      properties: { host: "unreachable.invalid", port: "5432", database: "orders" },
    },
  ],
};

const result = spawnSync(binary, ["publish-links"], {
  input: `${JSON.stringify(request)}\n`,
  encoding: "utf8",
  env: { ...process.env, AWS_REGION: region },
});

if (result.error) {
  die(`the publisher could not start: ${result.error.message}`);
}
if (result.status === 0) {
  console.error(`${LOG_PREFIX} absent-substrate: FAILED — the publish succeeded against ${region}, which holds no ocel bootstrap`);
  process.exit(1);
}
if (!namesAbsentSubstrate(result.stderr)) {
  console.error(`${LOG_PREFIX} absent-substrate: FAILED — the publisher exited ${result.status} saying ${JSON.stringify(result.stderr.trim())}, which never names the absent substrate`);
  process.exit(1);
}

console.error(`${LOG_PREFIX} absent-substrate: PASSED — ${result.stderr.trim()}`);
