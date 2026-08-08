#!/usr/bin/env node
// Asserts that a plain node app's V8 compile cache is complete in S3 *before
// the app serves anyone*, and that the app answers correctly — not merely
// that the deploy succeeded and a request answers 200. It does not prove a
// later cold start reads that object back from S3: embedding is unconditional
// whenever bytecode caching is on (cloud/aws/deploy/embed.go), so this
// deployment's own cold starts load the cache from their own artifact
// instead, and the S3 read leg goes SKIPPED — see the warning it logs, and
// assert-embed.mjs, which is what proves the read leg that actually runs.
//
// This is scripts/e2e-next/assert-bytecode.mjs's proof, unchanged in what it
// claims — see that file's own header for the full reasoning, which applies
// here verbatim: the object is whole before this script's first request, the
// deploy's own warm summary attributes it to the warm pass
// (cloud/aws/deploy/warm.go), and the summary's loaded/entries account for
// the whole bundle.
//
// What differs for a plain node app:
//
//   - There is no entry table to walk. loadUserApp
//     (packages/lambda-entrypoints/src/node/entrypoint.mts) imports the whole
//     module graph at INIT, before a warm invocation can ever reach the
//     handler, so warmNode reports that honestly — state "loaded-at-init",
//     no fabricated entry count — rather than pretending it walked one entry.
//     The Go side surfaces this as its own summary field
//     (wholeGraphLoadedAtInit) and the harness reads it into its own
//     falsifiable verdict, "whole-graph": see warmCoverage's own doc comment
//     for why that is distinct from, but ranks alongside, the "complete"
//     verdict a fully-covered Next bundle gets. This script additionally
//     asserts the deploy log carries that honest report rather than the
//     "node did not report back on the compile-cache warm" line a launcher
//     that never answered the warm request would have produced
//     (cloud/aws/cmd/lambdanode/bootstrap/warm.go's warmSummary.count).
//   - No framework here registers a Cloudflare worker
//     (cloud/edge/framework/registry.go), so the app is served straight from
//     its Lambda Function URL — AWS_IAM-authorized, like every Function URL
//     this deploy pipeline provisions (cloud/aws/deploy/function.go) — and
//     every request this script sends has to be SigV4-signed (sigv4.mjs) or
//     it 403s before the app ever sees it. scripts/e2e-next never needs this:
//     its requests land on a Cloudflare worker that signs its own forward.
//   - Bursting for fresh sandboxes needs no force-dynamic route: nothing
//     fronts this app with a response cache, so ECHO_ROUTE reaches the
//     Lambda on every request already — moot here since the read-leg burst
//     never runs (see above), but what scripts/e2e-next needs a force-dynamic
//     route for.
//   - Correctness is proven by a single signed request (assertCorrectness)
//     rather than folded into a burst: with the read leg always skipped there
//     is no burst here to fold it into.
//
// Usage: assert-bytecode.mjs [deployment-url]
//   falls back to $OCEL_E2E_NODE_DEPLOY_URL.
//   Run from the deployed app's directory: slug, environment, app name and
//   build id are read from .ocel/deploy-result.json there.
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
  ECHO_MARKER,
  ECHO_ROUTE,
  HEALTH_ROUTE,
  WARM_SUMMARY_MARKER,
  bytecodeAppNamespace,
  bytecodeCacheEntry,
  echoValue,
  strongestCoverage,
  tarEntryNames,
  warmCoverage,
  warmSummaryOutcome,
} from "./lib.mjs";
import { sigv4Fetch } from "./sigv4.mjs";

// See scripts/e2e-next/assert-bytecode.mjs's own constant for the full
// reasoning: generous on purpose, since the window only has to contain the
// summaries, each attributed by the key it names rather than by timing.
const WARM_LOG_LOOKBACK_MS = 30 * 60_000;

const WARM_LOG_FILTER = `"${WARM_SUMMARY_MARKER}"`;

// A fresh Function URL can refuse or 5xx briefly while it propagates — the
// same window cli/internal/cli/deploy_e2e_test.go's getHealthWithRetry absorbs
// against the real provider. Nothing in this harness's deploy.mjs waits this
// out itself, so the first assertion to run against a URL has to.
const HEALTH_RETRY_DEADLINE_MS = 3 * 60_000;

const warnings = [];

const base = process.argv[2] || process.env.OCEL_E2E_NODE_DEPLOY_URL;
if (!base) {
  fail("no deployment url given (argument or $OCEL_E2E_NODE_DEPLOY_URL)");
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

const signedFetch = sigv4Fetch({
  accessKeyId: process.env.AWS_ACCESS_KEY_ID,
  secretAccessKey: process.env.AWS_SECRET_ACCESS_KEY,
  sessionToken: process.env.AWS_SESSION_TOKEN,
});

const bucket = process.env.OCEL_ASSET_BUCKET || resolveBootstrapBucket("AssetBucket", "$OCEL_ASSET_BUCKET", fail);
const functionName = resolveFunctionName(result.slug, app.name, fail);
const namespace = bytecodeAppNamespace({ environment: result.environment, slug: result.slug, app: app.name });
const namespacePrefix = `${namespace}/`;
log(`expecting one object for ${functionName} under s3://${bucket}/${namespacePrefix}`);

await waitForHealthy();

// --- warm leg: the cache is whole before anyone hits the app ---------------

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
      `<hash>/node<version>-${LAMBDA_ARCH}.tar.gz — expected exactly one`,
  );
}
if (candidates.length === 0) {
  fail(
    `no object matching <hash>/node<version>-${LAMBDA_ARCH}.tar.gz exists under ` +
      `s3://${bucket}/${namespacePrefix}, and this script has not touched the deployment yet. Check the deploy ` +
      `output for its warm pass lines ("ocel: warmed N/M bundles"), and the function's CloudWatch logs for ` +
      `"ocel: warm invocation:".`,
  );
}
const found = candidates[0];
const key = `${namespace}/${found.hash}/${found.filename}`;
log(`s3://${bucket}/${key} already exists, before this script has issued a request`);

const warmLogStart = deployedAt - WARM_LOG_LOOKBACK_MS;
log(`polling CloudWatch for the deploy's warm summary since ${new Date(warmLogStart).toISOString()}`);
const warmDeadline = Date.now() + LOG_DEADLINE_MS;
const verdicts = [];
const seenWarmEventIds = new Set();
let warmLogsSucceeded = false;
let warmLogsError = null;
let sawUnreportedWarm = false;
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
    if (outcome.summary?.uncounted?.includes("did not report back")) {
      sawUnreportedWarm = true;
    }
    verdicts.push(verdict);
  }
  if (verdicts.length === 0) await sleep(LOG_POLL_INTERVAL_MS);
}
const coverage = strongestCoverage(verdicts);

if (!coverage && !warmLogsSucceeded) {
  fail(
    `could not read /aws/lambda/${functionName} logs at all within ${LOG_DEADLINE_MS / 1000}s — every attempt failed, ` +
      `so nothing here says what published s3://${bucket}/${key}: ${warmLogsError?.message}`,
  );
}
if (!coverage) {
  fail(
    `s3://${bucket}/${key} exists but no warm summary naming it appears in /aws/lambda/${functionName} within ` +
      `${WARM_LOG_LOOKBACK_MS / 60_000} minutes before the deploy completed.`,
  );
}
if (coverage.kind === "failed") {
  fail(`the deploy's warm pass did not publish this cache: ${coverage.detail}`);
}
if (coverage.kind === "unproven") {
  fail(
    `s3://${bucket}/${key} exists, but every warm summary for it reports already-cached (${coverage.detail}) — so no ` +
      `pass in this window both wrote the object and measured it.`,
  );
}
if (sawUnreportedWarm) {
  // Would have been silent under the pre-warm-report entrypoint: the object
  // still lands (the primary import always runs before a warm invocation can
  // reach the handler), but nothing said what it covered. A patched
  // entrypoint must never produce this line, so its presence here is a
  // regression worth failing loudly on rather than folding into `coverage`.
  fail(
    `the deploy's warm summary for ${key} reports "node did not report back on the compile-cache warm" — the node ` +
      `entrypoint's report-only warm handler (packages/lambda-entrypoints/src/node/entrypoint.mts) never answered the ` +
      `membrane's control-socket request. Check that this deployment's lambdanode membrane layer actually carries it.`,
  );
}
if (coverage.kind === "partial") {
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
  coverage.kind === "complete" || coverage.kind === "whole-graph"
    ? "bytecode cache published, whole, before the first request"
    : "bytecode cache published before the first request, covering part of the bundle",
);

// --- read leg (and correctness) --------------------------------------------

// Embedding is no longer its own gate (cloud/aws/deploy/embed.go): whenever
// bytecode caching is on at all, cold starts never fetch `key` from S3, so
// this leg is unconditionally dropped rather than made to fail a deployment
// working exactly as designed. assert-embed.mjs covers it in this script's
// place; a single signed request stands in for the correctness proof the
// (now permanently skipped) burst would otherwise have folded in.
warn(
  `read leg SKIPPED and UNPROVEN: embedding is unconditional whenever bytecode caching is on, so cold starts load ` +
    `the compile cache from the function's own artifact and no instance will ever report rehydrating ${key} from ` +
    `S3. Run assert-embed.mjs against this deployment — it asserts the embedded read leg, and nothing here does.`,
);
await assertCorrectness();
reportWarnings();

// --- helpers -----------------------------------------------------------

// waitForHealthy absorbs a fresh Function URL's propagation window before
// anything else here trusts it to answer — see HEALTH_RETRY_DEADLINE_MS.
async function waitForHealthy() {
  const target = new URL(HEALTH_ROUTE, base).toString();
  const deadline = Date.now() + HEALTH_RETRY_DEADLINE_MS;
  let lastError = null;
  while (Date.now() < deadline) {
    try {
      const res = await signedFetch(target);
      if (res.ok) {
        log(`${HEALTH_ROUTE} answered ${res.status}; the app is reachable`);
        return;
      }
      lastError = new Error(`HTTP ${res.status}`);
    } catch (err) {
      lastError = err;
    }
    await sleep(POLL_INTERVAL_MS);
  }
  fail(`${target} never answered healthy within ${HEALTH_RETRY_DEADLINE_MS / 1000}s: ${lastError?.message}`);
}

// burstEcho fires `n` concurrent signed GETs at ECHO_ROUTE and reports how
// many succeeded and, of those, how many carried ECHO_MARKER and their own
// echoed value — the correctness proof this harness folds into the same burst
// that forces fresh sandboxes for the read leg.
// A 429 is reported as its own outcome rather than folded into "not ok": the
// account's Lambda concurrency quota is lower than a full-size burst
// (REHYDRATE_BURST_SIZE in the scripts that burst with this), so a run that
// throttles some fraction of its requests is an expected, reportable outcome
// of that mismatch, not silently indistinguishable from every other kind of
// failure a caller has to guess at.
async function burstEcho(n, label) {
  const results = await Promise.all(
    Array.from({ length: n }, async (_, i) => {
      const value = echoValue(`${label}-${Date.now()}-${process.pid}-${i}`);
      const target = new URL(`${ECHO_ROUTE}?value=${encodeURIComponent(value)}`, base).toString();
      try {
        const res = await signedFetch(target);
        if (res.status === 429) return { ok: false, throttled: true };
        if (!res.ok) return { ok: false };
        const body = await res.json();
        return { ok: true, correct: body?.marker === ECHO_MARKER && body?.value === value };
      } catch {
        return { ok: false };
      }
    }),
  );
  const succeeded = results.filter((r) => r.ok).length;
  const correct = results.filter((r) => r.ok && r.correct).length;
  const throttled = results.filter((r) => r.throttled).length;
  return { succeeded, correct, throttled };
}

// assertCorrectness is the embed-path's stand-in for the burst's correctness
// check: with the read leg skipped there is no burst to fold it into, so a
// single signed request stands in for it instead.
async function assertCorrectness() {
  const { succeeded, correct } = await burstEcho(1, "correctness");
  if (succeeded === 0) {
    fail(`a single signed request to ${ECHO_ROUTE} failed — the app is not reachable at all`);
  }
  if (correct === 0) {
    fail(`${ECHO_ROUTE} answered without ${JSON.stringify(ECHO_MARKER)} in the body`);
  }
  log(`${ECHO_ROUTE} answered correctly`);
}

function reportWarnings() {
  if (!warnings.length) return;
  log(`passed with ${warnings.length} warning${warnings.length === 1 ? "" : "s"}:`);
  for (const warning of warnings) log(`  ${warning}`);
}

function listBytecodeObjects(bucket, prefix) {
  return listObjectKeys(bucket, prefix).map((key) => key.slice(prefix.length));
}

function log(message) {
  console.error(`[ocel-e2e-node] bytecode: ${message}`);
}

function warn(message) {
  warnings.push(message);
  console.error(`[ocel-e2e-node] bytecode WARNING: ${message}`);
}

function fail(message) {
  console.error(`[ocel-e2e-node] bytecode assertion failed: ${message}`);
  process.exit(1);
}
