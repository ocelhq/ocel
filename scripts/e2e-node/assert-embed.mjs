#!/usr/bin/env node
// Asserts that the deploy-time embed pass actually moved a plain node app's
// function onto a repackaged artifact carrying its own compile cache, that
// cold starts read that copy instead of fetching the object from S3, and
// that the app answers correctly with the cache in place.
//
// This is scripts/e2e-next/assert-embed.mjs's proof, unchanged in what it
// claims — see that file's own header for the three-part reasoning (repackaged
// CodeSha256, the tar under exactly the derived name, a burst that reads the
// embedded path and never S3), which applies here verbatim.
//
// What differs for a plain node app is exactly what differs in
// assert-bytecode.mjs: no CDN/edge cache tier means no force-dynamic route is
// needed to reach the Lambda, every request has to be SigV4-signed
// (sigv4.mjs) because the Function URL is AWS_IAM with no signing worker in
// front of it, and the burst's correctness check is folded in rather than
// separate. See assert-bytecode.mjs's own header for the fuller version of
// each of these.
//
// Skips, loudly, unless $OCEL_BYTECODE_EMBED=1.
//
// Usage: assert-embed.mjs [deployment-url]
//   falls back to $OCEL_E2E_NODE_DEPLOY_URL.
//   Run from the deployed app's directory: slug, environment, app name and
//   build id are read from .ocel/deploy-result.json there.
//   $OCEL_ASSET_BUCKET and $OCEL_ARTIFACT_BUCKET name the two buckets
//   involved; without them the script resolves the substrate's from the
//   preview bootstrap stack.
//
// Exits non-zero with the observations it collected.

import { createHash } from "node:crypto";
import { existsSync, readFileSync } from "node:fs";
import { join } from "node:path";

import {
  LAMBDA_ARCH,
  LIST_RETRY_DEADLINE_MS,
  LOG_DEADLINE_MS,
  LOG_PAGE_LIMIT,
  LOG_POLL_INTERVAL_MS,
  POLL_INTERVAL_MS,
  describeFunction,
  fetchFunctionLogs,
  getObject,
  listObjectKeys,
  resolveBootstrapBucket,
  resolveFunctionName,
  sleep,
} from "./aws.mjs";
import {
  BYTECODE_EMBED_ENV,
  BYTECODE_S3_REHYDRATE_MARKER,
  DEPLOY_RESULT_FILE,
  ECHO_MARKER,
  ECHO_ROUTE,
  HEALTH_ROUTE,
  bytecodeAppNamespace,
  bytecodeCacheEntry,
  bytecodeEmbedEnabled,
  bytecodeEmbeddedOutcome,
  echoValue,
  embeddedArtifactPairs,
  embeddedBytecodePath,
  logWindowVerdict,
  summarizeOutcomes,
  zipEntryNames,
} from "./lib.mjs";
import { sigv4Fetch } from "./sigv4.mjs";

const BURST_SIZE = 20;
const LOG_CONFIRM_DEADLINE_MS = 30_000;
const TASK_ROOT = "/var/task";
const BURST_LOG_FILTER = `?"embedded compile cache" ?"${BYTECODE_S3_REHYDRATE_MARKER.trim()}"`;
const MAX_PACKAGE_BYTES = 250 * 1024 * 1024;
const HEALTH_RETRY_DEADLINE_MS = 3 * 60_000;

if (!bytecodeEmbedEnabled(process.env)) {
  log(
    `SKIPPED, nothing asserted: $${BYTECODE_EMBED_ENV} is ${JSON.stringify(process.env[BYTECODE_EMBED_ENV] ?? null)}, ` +
      `not "1", so the deploy ran no embed pass and this deployment is expected to fetch its compile cache from S3. ` +
      `assert-bytecode.mjs is what proves that path. Re-deploy with ${BYTECODE_EMBED_ENV}=1 to make this assertion ` +
      `mean anything.`,
  );
  process.exit(0);
}

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

const signedFetch = sigv4Fetch({
  accessKeyId: process.env.AWS_ACCESS_KEY_ID,
  secretAccessKey: process.env.AWS_SECRET_ACCESS_KEY,
  sessionToken: process.env.AWS_SESSION_TOKEN,
});

const assetBucket =
  process.env.OCEL_ASSET_BUCKET || resolveBootstrapBucket("AssetBucket", "$OCEL_ASSET_BUCKET", fail);
const artifactBucket =
  process.env.OCEL_ARTIFACT_BUCKET || resolveBootstrapBucket("ArtifactBucket", "$OCEL_ARTIFACT_BUCKET", fail);
const functionName = resolveFunctionName(result.slug, app.name, fail);

await waitForHealthy();

// --- what the membrane would look for -------------------------------------

const namespace = bytecodeAppNamespace({ environment: result.environment, slug: result.slug, app: app.name });
const namespacePrefix = `${namespace}/`;
const cacheNames = (await listRetrying(assetBucket, namespacePrefix)).map((full) => full.slice(namespacePrefix.length));
const candidates = cacheNames
  .map((name) => bytecodeCacheEntry(name))
  .filter((entry) => entry && entry.functionName === functionName && entry.arch === LAMBDA_ARCH);
if (candidates.length !== 1) {
  fail(
    `expected exactly one object matching <hash>/bytecode/${functionName}/node<version>-${LAMBDA_ARCH}.tar.gz under ` +
      `s3://${assetBucket}/${namespacePrefix}, found ${candidates.length}. Without the published cache there was ` +
      `nothing for the embed pass to embed — run assert-bytecode.mjs, which diagnoses the warm pass itself.`,
  );
}
const found = candidates[0];
const cacheKey = `${namespace}/${found.hash}/bytecode/${found.functionName}/${found.filename}`;
const entryName = embeddedBytecodePath(cacheKey);
if (!entryName) {
  fail(`could not derive an embedded tar path from ${cacheKey} — its name is not node<version>-<arch>.tar.gz`);
}
const taskPath = `${TASK_ROOT}/${entryName}`;
log(`the cache is published at s3://${assetBucket}/${cacheKey}, so the artifact must carry ${entryName}`);

// --- 1: the function is running repackaged code ----------------------------

const artifactPrefix = `${result.slug}/`;
const artifactKeys = await listRetrying(artifactBucket, artifactPrefix);
const pairs = embeddedArtifactPairs(artifactKeys);
if (pairs.length !== 1) {
  fail(
    `expected exactly one repackaged artifact (<hash>-bc-<digest>.zip) under s3://${artifactBucket}/${artifactPrefix}, ` +
      `found ${pairs.length}. Objects seen: ${artifactKeys.join(", ") || "(none)"}`,
  );
}
const { embedded: embeddedKey, original: originalKey } = pairs[0];
if (!originalKey) {
  fail(`${embeddedKey} has no original counterpart under s3://${artifactBucket}/${artifactPrefix}`);
}

const originalSha = shaBase64(readObject(artifactBucket, originalKey));
const fn = describeDeployedFunction(functionName);
const codeSha = fn.Configuration?.CodeSha256;
if (!codeSha) {
  fail(`lambda get-function reported no Configuration.CodeSha256 for ${functionName}`);
}
if (codeSha === originalSha) {
  fail(
    `${functionName} is still running the artifact the deploy originally uploaded (CodeSha256 ${codeSha}), even ` +
      `though ${embeddedKey} exists. Check the deploy output for the embed pass's per-bundle lines.`,
  );
}
log(`${functionName} CodeSha256 ${codeSha} differs from the originally-uploaded ${originalKey} (${originalSha})`);

// --- 2: the deployed package carries the tar -------------------------------

const location = fn.Code?.Location;
if (!location) {
  fail(`lambda get-function reported no Code.Location for ${functionName}, so its package cannot be inspected`);
}
const packageBytes = await downloadPackage(location);
const packageSha = shaBase64(packageBytes);
if (packageSha !== codeSha) {
  fail(
    `the package downloaded from ${functionName}'s Code.Location hashes to ${packageSha}, but the function reports ` +
      `CodeSha256 ${codeSha} — these are not the deployed bytes.`,
  );
}

let entries;
try {
  entries = zipEntryNames(packageBytes);
} catch (err) {
  fail(`${functionName}'s deployment package could not be read as a zip: ${err.message}`);
}
if (!entries.includes(entryName)) {
  const near = entries.filter((name) => name.startsWith(".ocel/bytecode/"));
  fail(
    `${functionName}'s deployment package has ${entries.length} entries but not ${entryName}` +
      (near.length ? `. It does carry ${near.join(", ")} instead.` : ` and nothing under .ocel/bytecode/ at all.`),
  );
}
log(`the deployed package carries ${entryName} among its ${entries.length} entries`);

// --- 3: cold starts read it, never S3, and answer correctly ----------------

const burstStart = Date.now();
log(`bursting ${BURST_SIZE} concurrent requests to force fresh sandboxes`);
const { succeeded: burstSucceeded, correct: burstCorrect } = await burstEcho(BURST_SIZE, "embed-burst");
log(`burst: ${burstSucceeded}/${BURST_SIZE} requests succeeded, ${burstCorrect}/${burstSucceeded} answered correctly`);
if (burstSucceeded === 0) {
  fail(`all ${BURST_SIZE} burst requests to ${ECHO_ROUTE} failed — the function was never invoked at all.`);
}
if (burstCorrect !== burstSucceeded) {
  fail(
    `${burstSucceeded - burstCorrect}/${burstSucceeded} successful burst requests to ${ECHO_ROUTE} answered without ` +
      `${JSON.stringify(ECHO_MARKER)} in the body.`,
  );
}
log(`all ${burstCorrect} successful burst requests answered correctly`);

log(`polling CloudWatch for ${LOG_DEADLINE_MS / 1000}s for every read-leg line the burst produced`);
const logDeadline = Date.now() + LOG_DEADLINE_MS;
const embeddedHits = [];
const embedFailures = [];
const s3Hits = [];
const seenEventIds = new Set();
let attempts = 0;
let failures = 0;
let logsError = null;
while (Date.now() < logDeadline) {
  attempts++;
  try {
    ingest(fetchFunctionLogs(functionName, burstStart, BURST_LOG_FILTER));
  } catch (err) {
    failures++;
    logsError = err;
    log(`could not read /aws/lambda/${functionName} logs (${err.message}); will retry`);
  }
  await sleep(LOG_POLL_INTERVAL_MS);
}

const confirmDeadline = Date.now() + LOG_CONFIRM_DEADLINE_MS;
let confirmed = false;
let finalEvents = 0;
for (;;) {
  attempts++;
  try {
    const events = fetchFunctionLogs(functionName, burstStart, BURST_LOG_FILTER);
    ingest(events);
    finalEvents = events.length;
    confirmed = true;
    break;
  } catch (err) {
    failures++;
    logsError = err;
    if (Date.now() >= confirmDeadline) break;
    log(`could not finish reading /aws/lambda/${functionName} logs (${err.message}); will retry`);
    await sleep(LOG_POLL_INTERVAL_MS);
  }
}

const coverage = logWindowVerdict({ attempts, failures, confirmed, events: finalEvents, pageLimit: LOG_PAGE_LIMIT });
if (coverage.kind === "unread") {
  fail(
    `could not read /aws/lambda/${functionName} to the end of the burst window within ` +
      `${(LOG_DEADLINE_MS + LOG_CONFIRM_DEADLINE_MS) / 1000}s (${coverage.detail}): ${logsError?.message}.`,
  );
}
if (coverage.kind === "truncated") {
  fail(`the read of /aws/lambda/${functionName} covering the burst window is truncated (${coverage.detail}).`);
}
if (failures) {
  log(`read the burst window despite ${failures} failed attempt${failures === 1 ? "" : "s"}: ${coverage.detail}`);
}

if (s3Hits.length) {
  fail(
    `${s3Hits.length} instance${s3Hits.length === 1 ? "" : "s"} fetched the compile cache from S3 after the burst ` +
      `(${embedFailures.length} embedded-leg failure line${embedFailures.length === 1 ? "" : "s"}` +
      `${embedFailures.length ? `: ${embedFailures.slice(0, 3).map((o) => o.message).join(" | ")}` : ""}). ` +
      `S3 lines: ${s3Hits.slice(0, 3).join(" | ")}`,
  );
}

if (embeddedHits.length === 0) {
  fail(
    `no instance reported loading the embedded compile cache from ${taskPath} within ${LOG_DEADLINE_MS / 1000}s of the ` +
      `burst, and none reported fetching from S3 either — ${summarizeOutcomes(embedFailures)} in ` +
      `/aws/lambda/${functionName}${
        embedFailures.length ? `; samples: ${embedFailures.slice(0, 5).map((o) => o.message).join(" | ")}` : ""
      }.`,
  );
}

log(`embedded hit: ${embeddedHits[0]}`);
log(
  `${embeddedHits.length} instance${embeddedHits.length === 1 ? "" : "s"} loaded the compile cache from the artifact, ` +
    `0 fetched it from S3 (${coverage.detail})`,
);
log("compile cache embedded in the deployment package and read from it end to end");

// --- helpers -----------------------------------------------------------

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

async function burstEcho(n, label) {
  const results = await Promise.all(
    Array.from({ length: n }, async (_, i) => {
      const value = echoValue(`${label}-${Date.now()}-${process.pid}-${i}`);
      const target = new URL(`${ECHO_ROUTE}?value=${encodeURIComponent(value)}`, base).toString();
      try {
        const res = await signedFetch(target);
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
  return { succeeded, correct };
}

function ingest(events) {
  for (const event of events) {
    if (event.eventId) {
      if (seenEventIds.has(event.eventId)) continue;
      seenEventIds.add(event.eventId);
    }
    const message = String(event.message ?? "");
    if (message.includes(BYTECODE_S3_REHYDRATE_MARKER)) s3Hits.push(message);
    const outcome = bytecodeEmbeddedOutcome(message, taskPath);
    if (!outcome) continue;
    if (outcome.kind === "hit") embeddedHits.push(outcome.message);
    else embedFailures.push(outcome);
  }
}

async function listRetrying(bucket, prefix) {
  const deadline = Date.now() + LIST_RETRY_DEADLINE_MS;
  for (;;) {
    try {
      return listObjectKeys(bucket, prefix);
    } catch (err) {
      if (Date.now() >= deadline) {
        fail(`could not list s3://${bucket}/${prefix} at all within ${LIST_RETRY_DEADLINE_MS / 1000}s: ${err.message}`);
      }
      log(`could not list s3://${bucket}/${prefix} (${err.message}); will retry`);
      await sleep(POLL_INTERVAL_MS);
    }
  }
}

function describeDeployedFunction(functionName) {
  try {
    return describeFunction(functionName);
  } catch (err) {
    fail(`could not describe lambda ${functionName}: ${err.message}`);
  }
}

function readObject(bucket, key) {
  try {
    return getObject(bucket, key, MAX_PACKAGE_BYTES);
  } catch (err) {
    fail(`could not read s3://${bucket}/${key}: ${err.message}`);
  }
}

async function downloadPackage(location) {
  let response;
  try {
    response = await fetch(location);
  } catch (err) {
    fail(`could not download ${functionName}'s deployment package: ${err.message}`);
  }
  if (!response.ok) {
    fail(`could not download ${functionName}'s deployment package: HTTP ${response.status}`);
  }
  const body = Buffer.from(await response.arrayBuffer());
  if (body.length > MAX_PACKAGE_BYTES) {
    fail(`${functionName}'s deployment package is ${body.length} bytes, past anything the embed pass could produce`);
  }
  return body;
}

function shaBase64(bytes) {
  return createHash("sha256").update(bytes).digest("base64");
}

function log(message) {
  console.error(`[ocel-e2e-node] embed: ${message}`);
}

function fail(message) {
  console.error(`[ocel-e2e-node] embed assertion failed: ${message}`);
  process.exit(1);
}
