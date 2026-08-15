#!/usr/bin/env node

import { readFileSync } from "node:fs";
import { join } from "node:path";

import {
  awsUnreachable,
  functionConfiguration,
  item,
  partitionRows,
  roleInlinePolicies,
  roleNameOf,
  taggedFunctionArns,
  varsTable,
} from "./aws.mjs";
import {
  CLASS,
  CONSUMER_STATE_FILE,
  DEPLOY_RESULT_FILE,
  LINK_NAME,
  LINK_TYPE,
  LOG_PREFIX,
  PUBLISHER,
  credentialLeakProblem,
  deliveredEnvProblem,
  grantsDeliveredProblem,
  linkEnvKey,
  linkIndexSortKey,
  linkPartitionKey,
  ownerProblem,
  parsePublishedRecord,
  recordSortKey,
  resolvedProblem,
  varsReachProblem,
} from "./lib.mjs";

const failures = [];

function check(what, problem) {
  if (problem) {
    failures.push(`${what}: ${problem}`);
    console.error(`${LOG_PREFIX} ${what}: FAILED — ${problem}`);
    return;
  }
  console.error(`${LOG_PREFIX} ${what}: ok`);
}

function die(message) {
  console.error(`${LOG_PREFIX} ${message}`);
  process.exit(2);
}

const unreachable = awsUnreachable();
if (unreachable) {
  die(`AWS is unreachable, so nothing here can be judged: ${unreachable}`);
}

const appDir = process.cwd();
const state = JSON.parse(readFileSync(join(appDir, CONSUMER_STATE_FILE), "utf8"));
const deployed = JSON.parse(readFileSync(join(appDir, DEPLOY_RESULT_FILE), "utf8"));

const publisherStatePath = process.env.OCEL_E2E_SST_STATE_FILE;
if (!publisherStatePath) {
  die("OCEL_E2E_SST_STATE_FILE is not set; it must name the publisher's state file so the consumed values can be checked against what was published");
}
const publisher = JSON.parse(readFileSync(publisherStatePath, "utf8"));
if (!publisher.outputs?.host) {
  die("the publisher's state records no host; run publish.mjs before this leg");
}

const table = varsTable();
if (!table) {
  die("this account holds no ocel bootstrap, so there is no store to read the record out of");
}

const rows = partitionRows(table, linkPartitionKey(state.slug, CLASS, LINK_NAME));
const record = rows[recordSortKey("")];

check("the publisher still owns the record the deploy consumed", ownerProblem(record, PUBLISHER));

const parsed = parsePublishedRecord(record);
check("the record still carries the publisher's own token", parsed.problem ?? (parsed.record.type === LINK_TYPE ? null : `the record names ${parsed.record.type}, want ${LINK_TYPE}`));

const ocelIndex = item(table, `PROJECT#${state.slug}#CLASS#${CLASS}`, linkIndexSortKey("OCEL", ""));
check(
  "ocel published no record of its own for a name it binds",
  (ocelIndex?.links?.SS ?? []).includes(LINK_NAME)
    ? `ocel's own index claims ${LINK_NAME}; a bound resource is consumed, never provisioned beside itself`
    : null,
);

const arns = taggedFunctionArns({ "ocel:project": state.slug, "ocel:app": state.app });
if (arns.length === 0) {
  die(`no lambda is tagged ocel:project=${state.slug} ocel:app=${state.app}; the deploy left nothing to inspect`);
}

const configurations = arns.map(functionConfiguration);
const documents = [
  ...new Set(configurations.map((c) => roleNameOf(c.Role))),
].flatMap(roleInlinePolicies);

for (const configuration of configurations) {
  const env = configuration.Environment?.Variables ?? {};
  check(
    `${configuration.FunctionName} is handed ${linkEnvKey(LINK_NAME)}`,
    deliveredEnvProblem(env, LINK_NAME),
  );
  check(
    `${configuration.FunctionName} carries no published credential in the clear`,
    credentialLeakProblem(env, [publisher.outputs.host, publisher.outputs.database]),
  );
}

check(
  "the published grants landed on the app's execution role",
  grantsDeliveredProblem(documents, parsed.record?.grants),
);
check(
  "the execution role reaches the link's own vars partition",
  varsReachProblem(documents, linkPartitionKey(state.slug, CLASS, LINK_NAME)),
);

const url = (deployed.appUrls ?? [])[0];
if (!url) {
  die("the deploy result names no app URL, so the running app cannot be asked what it resolved");
}

const reported = await report(url);
check(
  "the running app resolved the published resource under its declared name",
  resolvedProblem(reported, {
    host: publisher.outputs.host,
    port: publisher.outputs.port,
    database: publisher.outputs.database,
  }),
);
check(
  "the resolved connection string carries the published credential",
  reported?.hasPassword ? null : "the app resolved a connection string with no password; the published bag did not reach it whole",
);

if (failures.length > 0) {
  console.error(`${LOG_PREFIX} consumed: FAILED (${failures.length})`);
  process.exit(1);
}
console.error(`${LOG_PREFIX} consumed: PASSED`);

async function report(base) {
  const target = new URL("/link", base);
  const response = await fetch(target, { signal: AbortSignal.timeout(30_000) });
  if (!response.ok) {
    die(`${target} answered ${response.status}; the app came up without the resource it binds`);
  }
  return response.json();
}
