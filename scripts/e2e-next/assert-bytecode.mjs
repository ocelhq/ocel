#!/usr/bin/env node
// Asserts that a Next app's V8 compile cache is complete in S3 *before the app
// serves anyone* — not merely that the deploy succeeded and a request answers
// 200. It does not prove a later cold start reads that object back: embedding
// is unconditional whenever bytecode caching is on
// (cloud/aws/deploy/embed.go), so this deployment's own cold starts load the
// cache from their own artifact instead, and the S3 read leg goes SKIPPED —
// see the warning it logs, and assert-embed.mjs, which is what proves the
// read leg that actually runs.
//
// Why this exists as its own assertion: everything between a warm compile
// cache in /tmp and a reusable object in S3 is machinery no request can see —
// the flush-compile-cache control message, the tar+gzip+upload, and the HEAD
// guard that skips a redundant re-upload
// (cloud/aws/cmd/lambdanode/bootstrap/bytecode.go). A 200 from the deployment
// proves none of it.
//
// And the claim is stronger than "an object eventually appears". The upload is
// create-if-absent with no overwrite and no harvest, so whichever cold start
// publishes first decides that build's cache for its whole life; a Next bundle
// hides many routes behind a lazily-required entry table, so an organic first
// request would fix a cache covering the one route it touched. The deploy
// therefore invokes each function once, before the promote, to load the whole
// bundle (cloud/aws/deploy/warm.go). That is what this script asserts: the
// object is already there before it issues a single request of its own, the
// deploy's own warm summary attributes the object to that pass, and the
// summary's loaded/entries account for the whole bundle.
//
// Usage: assert-bytecode.mjs [deployment-url]
//   falls back to $NEXT_TEST_DEPLOY_URL, then $SMOKE_URL.
//   Run from the deployed app's directory: slug, environment, app name, build
//   id and deploy time are read from .ocel/deploy-result.json there — the same
//   document logs.mjs reads — rather than asked for by hand.
//   $OCEL_ASSET_BUCKET names the bucket the membrane uploads to; without it
//   the script resolves the substrate's from the preview bootstrap stack.
//
// Exits non-zero with the observations it collected.

import { existsSync, readFileSync } from "node:fs";
import { join } from "node:path";
import { gunzipSync } from "node:zlib";

import {
  LAMBDA_ARCH,
  LIST_RETRY_DEADLINE_MS,
  LOG_DEADLINE_MS,
  LOG_POLL_INTERVAL_MS,
  POLL_INTERVAL_MS,
  fetchFunctionLogs,
  getObject,
  listObjectKeys,
  resolveBootstrapBucket,
  resolveFunctionName,
  sleep,
} from "./aws.mjs";
import {
  DEPLOY_RESULT_FILE,
  WARM_SUMMARY_MARKER,
  bytecodeAppNamespace,
  bytecodeCacheEntry,
  strongestCoverage,
  tarEntryNames,
  warmCoverage,
  warmSummaryOutcome,
} from "./lib.mjs";

// Nothing is waited *for* here: the warm pass uploads inline, before the
// invocation it rides on answers, and the deploy does not return until every
// one of them has (cloud/aws/deploy/warm.go). So the object exists by the time
// this script starts, and a successful list that does not find it is a real
// miss to fail on rather than something to poll away. LIST_RETRY_DEADLINE_MS is
// only for retrying a list that could not be made at all.

// How far before the deploy's completion the warm summaries are looked for.
// The pass runs after the last app stack succeeds and before stageAndPromote,
// so its lines always predate `deployedAt`; the pass itself is capped at three
// minutes, but the staging and promotion between it and the result document
// being written are not bounded from out here. Generous on purpose: the window
// only has to contain the summaries, and each is attributed by the key it
// names, not by when it landed.
const WARM_LOG_LOOKBACK_MS = 30 * 60_000;

// The CloudWatch filter that leaves only the summary lines. Without it the
// window would be every line every instance of this function ever logged, and
// the 1000-event page below would fill with them before reaching a summary.
const WARM_LOG_FILTER = `"${WARM_SUMMARY_MARKER}"`;

const warnings = [];

const base = process.argv[2] || process.env.NEXT_TEST_DEPLOY_URL || process.env.SMOKE_URL;
if (!base) {
  fail("no deployment url given (argument, $NEXT_TEST_DEPLOY_URL or $SMOKE_URL)");
}

const resultPath = join(process.cwd(), DEPLOY_RESULT_FILE);
if (!existsSync(resultPath)) {
  fail(`${resultPath} not found — run this from the deployed app's directory, after a deploy`);
}
const result = JSON.parse(readFileSync(resultPath, "utf8"));
const app = result.apps?.[0];
if (!result.slug || !app?.name || !app?.buildId) {
  fail(`${resultPath} is missing slug/app name/build id: ${JSON.stringify(result)}`);
}
const deployedAt = Date.parse(result.deployedAt ?? "");
if (!Number.isFinite(deployedAt)) {
  fail(`${resultPath} carries no readable deployedAt (${JSON.stringify(result.deployedAt)}) — nothing here can say when the warm pass ran`);
}

const bucket = process.env.OCEL_ASSET_BUCKET || resolveBootstrapBucket("AssetBucket", "$OCEL_ASSET_BUCKET", fail);
const functionName = resolveFunctionName(result.slug, app.name, fail);
// The content-hash segment bytecodePrefixFor appends below this namespace
// (cloud/aws/deploy/bytecode.go) is not composed here — nothing outside a
// running deploy can compute hashArtifact's hash ahead of time — so this
// script lists the whole namespace and discovers it from what is actually
// there, one level down.
const namespace = bytecodeAppNamespace({ environment: result.environment, slug: result.slug, app: app.name });
const namespacePrefix = `${namespace}/`;
log(`expecting one object for ${functionName} under s3://${bucket}/${namespacePrefix}`);

// --- warm leg: the cache is whole before anyone hits the app ---------------

// Read before this script sends a single request, which is the whole point:
// an object here now cannot have been produced by anything this script did.
// The namespace carries the project's own slug, so it also cannot be an
// earlier run's leftover — a build's key is only ever writable by that
// build's deploy.
let names = null;
let listError = null;
const listDeadline = Date.now() + LIST_RETRY_DEADLINE_MS;
while (names === null) {
  try {
    names = listBytecodeObjects(bucket, namespacePrefix);
  } catch (err) {
    listError = err;
    if (Date.now() >= listDeadline) {
      fail(
        `could not list s3://${bucket}/${namespacePrefix} at all within ${LIST_RETRY_DEADLINE_MS / 1000}s — every attempt ` +
          `failed, so nothing here says whether the object exists: ${listError.message}`,
      );
    }
    log(`could not list s3://${bucket}/${namespacePrefix} (${err.message}); will retry`);
    await sleep(POLL_INTERVAL_MS);
  }
}

const candidates = names.map((name) => bytecodeCacheEntry(name)).filter((entry) => entry && entry.arch === LAMBDA_ARCH);
if (candidates.length > 1) {
  fail(
    `found ${candidates.length} objects under s3://${bucket}/${namespacePrefix} matching ` +
      `<hash>/node<version>-${LAMBDA_ARCH}.tar.gz ` +
      `(${candidates.map((c) => `${namespacePrefix}${c.hash}/${c.filename}`).join(", ")}) — expected exactly one`,
  );
}
if (candidates.length === 0) {
  fail(
    `no object matching <hash>/node<version>-${LAMBDA_ARCH}.tar.gz exists under ` +
      `s3://${bucket}/${namespacePrefix}, and this script has not touched the deployment yet. The deploy warms every ` +
      `bytecode-gated bundle before it promotes and does not return until each warm invocation has answered, so a ` +
      `missing object means the warm pass never published one — check the deploy output for its per-bundle lines ` +
      `("warmed N/M bundles"), and the function's CloudWatch logs for "ocel: warm invocation:".`,
  );
}
const found = candidates[0];
const key = `${namespace}/${found.hash}/${found.filename}`;
log(`s3://${bucket}/${key} already exists, before this script has issued a request`);

// That the object exists says nothing about *what* published it or how much of
// the bundle is in it, and both are the claim. Only the membrane's own summary
// answers either: published/uploaded:true is the create-if-absent PUT that
// created this key, which no other writer can also have made, and loaded
// against entries is how much of the bundle that PUT covers.
const warmLogStart = deployedAt - WARM_LOG_LOOKBACK_MS;
log(`polling CloudWatch for the deploy's warm summary since ${new Date(warmLogStart).toISOString()}`);
const warmDeadline = Date.now() + LOG_DEADLINE_MS;
const verdicts = [];
const seenWarmEventIds = new Set();
let coverage = null;
let warmLogsSucceeded = false;
let warmLogsError = null;
// Polled only until a summary for this key turns up: the deploy has already
// finished, so every summary it produced is in the log group by now and one
// successful read returns all of them at once. What is being waited out here
// is CloudWatch's ingestion lag, not a second summary arriving later.
while (Date.now() < warmDeadline && verdicts.length === 0) {
  let events;
  try {
    events = fetchFunctionLogs(functionName, warmLogStart, WARM_LOG_FILTER);
    warmLogsSucceeded = true;
  } catch (err) {
    warmLogsError = err;
    log(`could not read /aws/lambda/${functionName} logs (${err.message}); will retry`);
    await sleep(LOG_POLL_INTERVAL_MS);
    continue;
  }
  for (const event of events) {
    if (event.eventId) {
      if (seenWarmEventIds.has(event.eventId)) continue;
      seenWarmEventIds.add(event.eventId);
    }
    const outcome = warmSummaryOutcome(event.message);
    if (!outcome) continue;
    if (outcome.kind === "unreadable") {
      warn(`a warm summary in /aws/lambda/${functionName} could not be read as JSON (${outcome.reason}): ${outcome.message}`);
      continue;
    }
    const verdict = warmCoverage(outcome.summary, key);
    if (verdict.kind === "other-build") continue;
    verdicts.push(verdict);
  }
  if (verdicts.length === 0) await sleep(LOG_POLL_INTERVAL_MS);
}
coverage = strongestCoverage(verdicts);

if (!coverage && !warmLogsSucceeded) {
  fail(
    `could not read /aws/lambda/${functionName} logs at all within ${LOG_DEADLINE_MS / 1000}s — every attempt failed, ` +
      `so nothing here says what published s3://${bucket}/${key}: ${warmLogsError?.message}`,
  );
}
if (!coverage) {
  fail(
    `s3://${bucket}/${key} exists but no warm summary naming it appears in /aws/lambda/${functionName} within ` +
      `${WARM_LOG_LOOKBACK_MS / 60_000} minutes before the deploy completed. The object is then unattributed: it may ` +
      `be the deploy's warm pass with its logs lost, or it may be a request that reached this build before the ` +
      `promote and fixed a one-route cache — which is exactly what warming exists to prevent, and what this ` +
      `assertion cannot tell apart without the summary.`,
  );
}
if (coverage.kind === "failed") {
  fail(`the deploy's warm pass did not publish this cache: ${coverage.detail}`);
}
if (coverage.kind === "unproven") {
  fail(
    `s3://${bucket}/${key} exists, but every warm summary for it reports already-cached (${coverage.detail}) — so no ` +
      `pass in this window both wrote the object and measured it. Either something published this build's cache ` +
      `before the deploy warmed it, or the pass that did is older than the ${WARM_LOG_LOOKBACK_MS / 60_000}-minute ` +
      `window (a redeploy of an already-warmed build).`,
  );
}
if (coverage.kind === "partial") {
  // Loud, but not a failure: a bundle the ceiling or the deadline cuts short
  // still publishes a real cache, and this is the only place that outcome
  // surfaces at all. It is a fact about the app's size, not a broken cache.
  warn(`the warm pass did not cover the whole bundle: ${coverage.detail}`);
} else {
  log(`the deploy's warm pass published this object and covered the whole bundle: ${coverage.detail}`);
}

const body = getObject(bucket, key);
let archive;
try {
  archive = gunzipSync(body);
} catch (err) {
  fail(`s3://${bucket}/${key} (${body.length} bytes) is not valid gzip: ${err.message}`);
}

let entryNames;
try {
  entryNames = tarEntryNames(archive);
} catch (err) {
  fail(`s3://${bucket}/${key} decompresses to ${archive.length} bytes that are not a valid tar: ${err.message}`);
}
if (entryNames.length === 0) {
  fail(`s3://${bucket}/${key} decompresses to an empty tar — no compile cache was actually archived`);
}
if (!entryNames.some((name) => name.includes("/"))) {
  fail(
    `s3://${bucket}/${key} has no entry under a subdirectory (${entryNames.join(", ")}) — node nests its ` +
      `compile cache under a version-hash directory, so a flat archive means nothing real was cached`,
  );
}

log(
  `s3://${bucket}/${key}: valid gzip+tar, ${entryNames.length} entr${entryNames.length === 1 ? "y" : "ies"}, ` +
    `nested under a subdirectory`,
);
log(
  coverage.kind === "complete"
    ? "bytecode cache published, whole, before the first request"
    : "bytecode cache published before the first request, covering part of the bundle",
);

// --- read leg ----------------------------------------------------------

// The read leg below requires the S3 rehydrate line, which embedding makes
// false rather than merely unlikely: embedding is no longer its own gate
// (cloud/aws/deploy/embed.go) — whenever bytecode caching is on at all, the
// deploy bakes this same cache into the function's own artifact, and a cold
// start loads it from /var/task, never fetching `key` from S3. Requiring the
// line here would fail a deployment that is working exactly as designed, so
// the leg is unconditionally dropped — loudly, and named as unproven, because
// a skipped assertion that reads as a pass is the one failure mode worth more
// than the assertion itself.
//
// The write leg above stays valid regardless and is deliberately not skipped:
// the embed pass runs *after* the warm pass and reads what it published, so
// the object and its warm summary must exist either way.
warn(
  `read leg SKIPPED and UNPROVEN: embedding is unconditional whenever bytecode caching is on, so cold starts load ` +
    `the compile cache from the function's own artifact and no instance will ever report rehydrating ${key} from ` +
    `S3. Run assert-embed.mjs against this deployment — it asserts the embedded read leg, and nothing here does.`,
);
reportWarnings();

// Warnings are re-printed at the end because the run that produces one is a
// passing run: buried a hundred lines up, a bundle that only half warmed — or
// a leg that did not run at all — would be indistinguishable from one that
// warmed whole and proved everything.
function reportWarnings() {
  if (!warnings.length) return;
  log(`passed with ${warnings.length} warning${warnings.length === 1 ? "" : "s"}:`);
  for (const warning of warnings) log(`  ${warning}`);
}

// --- AWS -------------------------------------------------------------------

// listBytecodeObjects returns the names under `prefix` rather than whole keys:
// what varies between the objects here is node<version>-<arch>.tar.gz, and
// bytecodeCacheKeyName reads that name.
function listBytecodeObjects(bucket, prefix) {
  return listObjectKeys(bucket, prefix).map((key) => key.slice(prefix.length));
}

function log(message) {
  console.error(`[ocel-e2e] bytecode: ${message}`);
}

function warn(message) {
  warnings.push(message);
  console.error(`[ocel-e2e] bytecode WARNING: ${message}`);
}

function fail(message) {
  console.error(`[ocel-e2e] bytecode assertion failed: ${message}`);
  process.exit(1);
}
