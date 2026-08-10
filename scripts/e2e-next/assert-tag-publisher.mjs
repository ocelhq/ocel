#!/usr/bin/env node
// Asserts that a tag invalidation raised at the origin is actually *carried* by
// the account-level tag-snapshot publisher — not merely that the route that
// raised it answered 200.
//
// Why this exists as its own assertion: from ocelhq-wvag.6 the publisher is the
// fleet's only guarantor of on-demand invalidation. Everything between the raise
// and the edge — the DynamoDB stream, its filter, the event source mapping, the
// consumer, the S3 copy of the build's tag clock — is machinery no request
// touches. If any link is broken, every route still serves correctly and only
// invalidation is dead, silently. A 200 from the raising route cannot see that;
// the record appearing in the published snapshot can.
//
// Usage: assert-tag-publisher.mjs [deployment-url]
//   falls back to $NEXT_TEST_DEPLOY_URL, then $SMOKE_URL.
//   $OCEL_ASSET_BUCKET names the bucket the publisher writes; without it the
//   script resolves the substrate's from the preview bootstrap stack.
//
// Exits non-zero with the observations it collected.

import { execFileSync } from "node:child_process";

import { AWS_CLI_RETRY_ENV } from "./aws.mjs";
import { TAG_PROBE_ROUTE, tagProbeTag } from "./lib.mjs";

// The publisher is a stream consumer with a zero batching window, so the record
// should land in seconds. The deadline is generous against a cold start plus the
// stream's own propagation, and the interval is loose because each poll lists a
// bucket.
const PUBLISH_DEADLINE_MS = 180_000;
const POLL_INTERVAL_MS = 5_000;

const base = process.argv[2] || process.env.NEXT_TEST_DEPLOY_URL || process.env.SMOKE_URL;
if (!base) {
  fail("no deployment url given (argument, $NEXT_TEST_DEPLOY_URL or $SMOKE_URL)");
}

const bucket = process.env.OCEL_ASSET_BUCKET || resolveAssetBucket();
const tag = tagProbeTag(`${Date.now()}-${process.pid}`);
const target = new URL(TAG_PROBE_ROUTE + `?tag=${encodeURIComponent(tag)}`, base).toString();

// Read first, so "the tag is in the snapshot" can only mean this run put it
// there. A tag already present before the raise proves nothing about the
// publisher.
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

// A record that arrived by way of the dead-letter queue is not a success: it
// means the batch failed its retries and something else republished it.
const dlq = deadLetterDepth();
if (dlq > 0) {
  fail(`the publisher's dead-letter queue holds ${dlq} message(s); invalidations are being dropped`);
}

log("tag publisher carried the invalidation end to end");

// snapshotsCarrying lists every published tag clock in the bucket and answers
// with the keys of those whose records name `tag`. Listing rather than
// addressing one build's key: the assertion should fail loudly if the publisher
// wrote the right record under the wrong identity, which a direct GET of a key
// derived the same way the publisher derives it could never show.
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
      // A snapshot this script cannot parse is not this script's to report:
      // the publisher refuses to merge into one, so it would already be
      // failing its batches and the dead-letter check will say so.
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

// resolveAssetBucket finds the substrate's asset bucket the same way the
// publisher is given it: from the preview bootstrap stack, rather than by
// guessing at a name.
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

// Local rather than aws.mjs's wrapper because this script's calls run without
// that module's per-call timeout, but it borrows its retry env: the poll below
// lists a bucket every few seconds for three minutes, which is exactly the
// shape of traffic that earns a throttle, and a throw here aborts the whole
// assertion rather than costing it one attempt.
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
