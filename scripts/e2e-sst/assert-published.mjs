#!/usr/bin/env node

import { readFileSync } from "node:fs";
import { join } from "node:path";

import { awsUnreachable, item, partitionRows, varsTable } from "./aws.mjs";
import {
  CLASS,
  LINK_NAME,
  LINK_TYPE,
  LOG_PREFIX,
  PUBLISHER,
  STATE_FILE,
  grantProblem,
  linkPartitionKey,
  ownerProblem,
  pairProblem,
  parsePublishedRecord,
  linkIndexSortKey,
  recordSortKey,
  refuseUntilRePlumbed,
  valueSortKey,
} from "./lib.mjs";

refuseUntilRePlumbed();

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

const state = JSON.parse(readFileSync(join(process.cwd(), STATE_FILE), "utf8"));
const table = varsTable();
if (!table) {
  die("this account holds no ocel bootstrap, so there is no store to read the record out of");
}

const pk = linkPartitionKey(state.project, CLASS, LINK_NAME);
const rows = partitionRows(table, pk);

check(
  "the pair is whole",
  pairProblem({ record: rows[recordSortKey("")], value: rows[valueSortKey("")] }),
);

check("the record row is stamped with the publisher that wrote it", ownerProblem(rows[recordSortKey("")], PUBLISHER));

const parsed = parsePublishedRecord(rows[recordSortKey("")]);
check("the bag parses and names a type", parsed.problem ?? null);

if (parsed.record) {
  check(
    "the record carries the publisher's own token",
    parsed.record.type === LINK_TYPE
      ? null
      : `the record names ${parsed.record.type}, want ${LINK_TYPE}`,
  );
  check(
    "the token is not one ocel's own provisioning mints",
    parsed.record.type.startsWith("ocel:")
      ? `${parsed.record.type} is in the ocel namespace, which a publisher may not mint`
      : null,
  );
  check("every grant is scoped to the resource the link names", grantProblem(parsed.record.grants));
}

check(
  "the value is sealed rather than stored in the clear",
  rows[valueSortKey("")]?.ciphertext?.B ? null : "the value row carries no ciphertext",
);

const index = item(table, `PROJECT#${state.project}#CLASS#${CLASS}`, linkIndexSortKey(PUBLISHER, ""));
check("the publisher owns what it published", index ? null : "the publisher recorded no index, so nothing can prune it");
if (index) {
  const owned = index.links?.SS ?? [];
  check(
    "one record per consumable resource, no constituents",
    owned.length === 1 && owned[0] === LINK_NAME
      ? null
      : `the publisher owns ${JSON.stringify(owned)}, want exactly ["${LINK_NAME}"]`,
  );
}

if (failures.length > 0) {
  console.error(`${LOG_PREFIX} published: FAILED (${failures.length})`);
  process.exit(1);
}
console.error(`${LOG_PREFIX} published: PASSED`);
