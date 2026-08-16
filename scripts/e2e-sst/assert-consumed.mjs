#!/usr/bin/env node

import { readFileSync } from "node:fs";
import { join } from "node:path";

import {
  awsUnreachable,
  functionConfiguration,
  item,
  partitionRows,
  roleAttachedPolicyArns,
  roleInlinePolicies,
  roleNameOf,
  taggedFunctionArns,
  varsTable,
} from "./aws.mjs";
import {
  CLASS,
  CONSUMER_STATE_FILE,
  CUSTOM_LINK_NAME,
  CUSTOM_LINK_TYPE,
  DEPLOY_RESULT_FILE,
  LINK_NAME,
  LOG_PREFIX,
  credentialLeakProblem,
  deliveredEnvProblem,
  grantsDeliveredProblem,
  linkEnvKey,
  linkIndexSortKey,
  linkOwner,
  linkPartitionKey,
  ownerProblem,
  parsePublishedRecord,
  publishedIds,
  recordShapeProblem,
  recordSortKey,
  resolvedProblem,
  varsReachProblem,
  vpcAccessProblem,
  vpcPlacementProblem,
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
if (!publisher.outputs?.subnetIds || !publisher.outputs?.securityGroupIds) {
  die("the publisher's state records no network ids; the transform's placement has nothing to be judged against");
}
if (!publisher.stage) {
  die("the publisher's state records no stage, and the owner of a published link is the deployed resource's URN");
}

const publishingApp = publisher.app ?? publisher.project;
const owner = linkOwner({ app: publishingApp, stage: publisher.stage });
const customOwner = linkOwner({ app: publishingApp, stage: publisher.stage, link: CUSTOM_LINK_NAME });

const table = varsTable();
if (!table) {
  die("this account holds no ocel bootstrap, so there is no store to read the record out of");
}

const rows = partitionRows(table, linkPartitionKey(state.slug, CLASS, LINK_NAME));
const record = rows[recordSortKey("")];

check("the SST resource still owns the record the deploy consumed", ownerProblem(record, owner));

const parsed = parsePublishedRecord(record);
check("the record still names its type and its source", parsed.problem ?? recordShapeProblem(parsed.record));

const customRecord = partitionRows(table, linkPartitionKey(state.slug, CLASS, CUSTOM_LINK_NAME))[recordSortKey("")];
check(
  "the SST resource still owns the custom record the transform read",
  ownerProblem(customRecord, customOwner),
);
const parsedCustom = parsePublishedRecord(customRecord);
check(
  "the custom record still names its type and its source",
  parsedCustom.problem ??
    recordShapeProblem(parsedCustom.record, { name: CUSTOM_LINK_NAME, type: CUSTOM_LINK_TYPE }),
);

const ocelIndex = item(table, `PROJECT#${state.slug}#CLASS#${CLASS}`, linkIndexSortKey("OCEL", ""));
const ocelOwned = ocelIndex?.links?.SS ?? [];
check(
  "ocel published no record of its own for a name it binds",
  ocelOwned.includes(LINK_NAME)
    ? `ocel's own index claims ${LINK_NAME}; a bound resource is consumed, never provisioned beside itself`
    : null,
);
check(
  "ocel published no record of its own for a name only a transform reads",
  ocelOwned.includes(CUSTOM_LINK_NAME)
    ? `ocel's own index claims ${CUSTOM_LINK_NAME}; a custom record comes from your own infrastructure and ocel never writes one`
    : null,
);

const arns = taggedFunctionArns({ "ocel:project": state.slug, "ocel:app": state.app });
if (arns.length === 0) {
  die(`no lambda is tagged ocel:project=${state.slug} ocel:app=${state.app}; the deploy left nothing to inspect`);
}

const configurations = arns.map(functionConfiguration);
const roles = [...new Set(configurations.map((c) => roleNameOf(c.Role)))];
const documents = roles.flatMap(roleInlinePolicies);

const network = {
  subnetIds: publishedIds(publisher.outputs.subnetIds),
  securityGroupIds: publishedIds(publisher.outputs.securityGroupIds),
};

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
  check(
    `${configuration.FunctionName} runs inside the network the custom record published`,
    vpcPlacementProblem(configuration, network),
  );
}

for (const role of roles) {
  check(
    `${role} carries the VPC-access policy a placed function needs`,
    vpcAccessProblem(roleAttachedPolicyArns(role)),
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
