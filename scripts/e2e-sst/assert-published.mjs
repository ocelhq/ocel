#!/usr/bin/env node

import { spawnSync } from "node:child_process";
import { readFileSync } from "node:fs";
import { join } from "node:path";

import { awsUnreachable, item, partitionRows, varsTable } from "./aws.mjs";
import {
  CLASS,
  LINK_NAME,
  LOG_PREFIX,
  STATE_FILE,
  grantProblem,
  linkIndexSortKey,
  linkOwner,
  linkPartitionKey,
  listedLinkProblem,
  ownerProblem,
  pairProblem,
  parsePublishedRecord,
  recordShapeProblem,
  recordSortKey,
  redactionProblem,
  valueSortKey,
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

const adapterDir = process.env.ADAPTER_DIR;
if (!adapterDir) {
  die("ADAPTER_DIR is not set; it must point at this repository");
}

const unreachable = awsUnreachable();
if (unreachable) {
  die(`AWS is unreachable, so nothing here can be judged: ${unreachable}`);
}

const state = JSON.parse(readFileSync(join(process.cwd(), STATE_FILE), "utf8"));
if (!state.stage) {
  die("the state file records no stage; run publish.mjs before this leg, since the owner is the deployed resource's URN");
}

const owner = linkOwner({ app: state.app ?? state.project, stage: state.stage });

const listed = spawnSync(
  process.execPath,
  [join(adapterDir, "packages", "ocel", "bin", "run.js"), "link", "ls", "--log-format", "json"],
  { cwd: state.projectDir, encoding: "utf8" },
);
if (listed.status !== 0) {
  die(`ocel link ls exited ${listed.status} in ${state.projectDir}: ${String(listed.stderr ?? "").trim()}`);
}
check("the published link is the one the project lists", listedLinkProblem(links(listed.stdout), { owner }));

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

check("the record row is stamped with the resource that wrote it", ownerProblem(rows[recordSortKey("")], owner));

const parsed = parsePublishedRecord(rows[recordSortKey("")]);
check("the record parses and names a type", parsed.problem ?? null);

if (parsed.record) {
  check("the record is the postgres link this suite published", recordShapeProblem(parsed.record));
  check("the record beside the sealed value holds no property", redactionProblem(parsed.record));
  check("every grant is scoped to the resource the link names", grantProblem(parsed.record.grants));
  check(
    "the grant the SST config described survived the publish",
    (parsed.record.grants ?? []).some((g) => (g.actions ?? []).includes("rds-db:connect"))
      ? null
      : "the record grants no rds-db:connect, so the consuming app would receive values it cannot use",
  );
}

check(
  "the value is sealed rather than stored in the clear",
  rows[valueSortKey("")]?.ciphertext?.B ? null : "the value row carries no ciphertext",
);

const index = item(table, `PROJECT#${state.project}#CLASS#${CLASS}`, linkIndexSortKey(owner, ""));
check("the resource owns what it published", index ? null : "the publish recorded no index, so nothing can prune it");
if (index) {
  const owned = index.links?.SS ?? [];
  check(
    "one record per link resource, no constituents",
    owned.length === 1 && owned[0] === LINK_NAME
      ? null
      : `the resource owns ${JSON.stringify(owned)}, want exactly ["${LINK_NAME}"]`,
  );
}

if (failures.length > 0) {
  console.error(`${LOG_PREFIX} published: FAILED (${failures.length})`);
  process.exit(1);
}
console.error(`${LOG_PREFIX} published: PASSED`);

function links(stdout) {
  const raw = String(stdout ?? "");
  const start = raw.indexOf("{");
  if (start < 0) {
    die(`ocel link ls printed no JSON: ${raw.trim()}`);
  }
  try {
    return JSON.parse(raw.slice(start)).links ?? [];
  } catch (err) {
    die(`ocel link ls printed unparseable JSON: ${err.message}`);
  }
}
