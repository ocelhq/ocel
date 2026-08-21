#!/usr/bin/env node

import { execFileSync } from "node:child_process";

import { AWS_CLI_RETRY_ENV } from "./aws.mjs";
import { TAG_PROBE_ROUTE, tagProbeTag } from "./lib.mjs";

const PUBLISH_DEADLINE_MS = 180_000;
const POLL_INTERVAL_MS = 5_000;

const base = process.argv[2] || process.env.NEXT_TEST_DEPLOY_URL;
if (!base) {
  fail("no deployment url given (argument or $NEXT_TEST_DEPLOY_URL)");
}

const bucket = process.env.OCEL_ASSET_BUCKET || resolveAssetBucket();
const tag = tagProbeTag(`${Date.now()}-${process.pid}`);
const target = new URL(TAG_PROBE_ROUTE + `?tag=${encodeURIComponent(tag)}`, base).toString();

const before = snapshotsCarrying(tag);
if (before.length > 0) {
  fail(`tag ${tag} was already published before this run raised it, in ${before.join(", ")}`);
}

log(`raising ${tag} via ${target}`);
const response = await fetch(target, { method: "POST" });
if (!response.ok) {
  fail(`${target} answered ${response.status}; the origin never raised the tag at all`);
}
log(`raised at ${new Date().toISOString()}`);

const deadline = Date.now() + PUBLISH_DEADLINE_MS;
let carriers = [];
while (Date.now() < deadline) {
  await sleep(POLL_INTERVAL_MS);
  carriers = snapshotsCarrying(tag);
  if (carriers.length > 0) break;
  log(`not published yet, ${Math.round((deadline - Date.now()) / 1000)}s left`);
}

if (carriers.length === 0) {
  fail(
    `${tag} never reached a published tag snapshot in ${bucket} within ${PUBLISH_DEADLINE_MS / 1000}s. ` +
      `The raise itself succeeded, so the break is downstream: the stream, its filter, ` +
      `the event source mapping, or the publisher function.`,
  );
}
log(`published to ${carriers.join(", ")}`);

const dlq = deadLetterDepth();
if (dlq > 0) {
  fail(`the publisher's dead-letter queue holds ${dlq} message(s); invalidations are being dropped`);
}

log("tag publisher carried the invalidation end to end");

function snapshotsCarrying(tag) {
  const keys = aws([
    "s3api",
    "list-objects-v2",
    "--bucket",
    bucket,
    "--query",
    "Contents[?ends_with(Key, `/tag-clock.json`)].Key",
    "--output",
    "text",
  ])
    .split(/\s+/)
    .filter(Boolean);

  return keys.filter((key) => {
    const body = aws(["s3", "cp", `s3://${bucket}/${key}`, "-"]);
    let snapshot;
    try {
      snapshot = JSON.parse(body);
    } catch {
      return false;
    }
    return Object.hasOwn(snapshot?.records ?? {}, tag);
  });
}

function deadLetterDepth() {
  const url = aws(["sqs", "list-queues", "--query", "QueueUrls[?contains(@, `TagPublisherDeadLetter`)] | [0]", "--output", "text"]);
  if (!url || url === "None") return 0;
  const depth = aws([
    "sqs",
    "get-queue-attributes",
    "--queue-url",
    url,
    "--attribute-names",
    "ApproximateNumberOfMessages",
    "--query",
    "Attributes.ApproximateNumberOfMessages",
    "--output",
    "text",
  ]);
  return Number(depth) || 0;
}

function resolveAssetBucket() {
  const found = aws([
    "cloudformation",
    "describe-stack-resources",
    "--stack-name",
    process.env.OCEL_BOOTSTRAP_STACK || "ocel-bootstrap-preview",
    "--query",
    "StackResources[?LogicalResourceId==`AssetBucket`].PhysicalResourceId | [0]",
    "--output",
    "text",
  ]);
  if (!found || found === "None") {
    fail("could not resolve the substrate's asset bucket; set $OCEL_ASSET_BUCKET");
  }
  return found;
}

function aws(args) {
  return execFileSync("aws", args, {
    encoding: "utf8",
    maxBuffer: 64 * 1024 * 1024,
    env: { ...process.env, ...AWS_CLI_RETRY_ENV },
  }).trim();
}

function sleep(ms) {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

function log(message) {
  console.error(`[ocel-e2e] ${message}`);
}

function fail(message) {
  console.error(`[ocel-e2e] FAIL: ${message}`);
  process.exit(1);
}
