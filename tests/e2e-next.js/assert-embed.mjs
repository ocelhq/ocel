#!/usr/bin/env node

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
  TAG_PROBE_ROUTE,
  appAssetPrefix,
  bytecodeCacheKeyName,
  bytecodeCacheKeyPrefix,
  bytecodeEmbedEnabled,
  bytecodeEmbeddedOutcome,
  embeddedArtifactPairs,
  embeddedBytecodePath,
  logWindowVerdict,
  summarizeOutcomes,
  tagProbeTag,
  zipEntryNames,
} from "./lib.mjs";

const BURST_SIZE = 20;

const LOG_CONFIRM_DEADLINE_MS = 30_000;

const TASK_ROOT = "/var/task";

const BURST_LOG_FILTER = `?"embedded compile cache" ?"${BYTECODE_S3_REHYDRATE_MARKER.trim()}"`;

const MAX_PACKAGE_BYTES = 250 * 1024 * 1024;

if (!bytecodeEmbedEnabled(process.env)) {
  log(
    `SKIPPED, nothing asserted: $${BYTECODE_EMBED_ENV} is ${JSON.stringify(process.env[BYTECODE_EMBED_ENV] ?? null)}, ` +
      `not "1", so the deploy ran no embed pass and this deployment is expected to fetch its compile cache from S3. ` +
      `assert-bytecode.mjs is what proves that path. Re-deploy with ${BYTECODE_EMBED_ENV}=1 to make this assertion ` +
      `mean anything.`,
  );
  process.exit(0);
}

const base = process.argv[2] || process.env.NEXT_TEST_DEPLOY_URL;
if (!base) {
  fail("no deployment url given (argument or $NEXT_TEST_DEPLOY_URL)");
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

const assetBucket =
  process.env.OCEL_ASSET_BUCKET || resolveBootstrapBucket("AssetBucket", "$OCEL_ASSET_BUCKET", fail);
const artifactBucket =
  process.env.OCEL_ARTIFACT_BUCKET || resolveBootstrapBucket("ArtifactBucket", "$OCEL_ARTIFACT_BUCKET", fail);
const functionName = resolveFunctionName(result.slug, app.name, result.environment, fail);

const keyPrefix = bytecodeCacheKeyPrefix({
  prefix: appAssetPrefix({
    environment: result.environment,
    slug: result.slug,
    app: app.name,
    buildId: app.buildId,
  }),
  functionName,
});
const cacheNames = (await listRetrying(assetBucket, keyPrefix)).map((full) => full.slice(keyPrefix.length));
const candidates = cacheNames.filter((name) => bytecodeCacheKeyName(name)?.arch === LAMBDA_ARCH);
if (candidates.length !== 1) {
  fail(
    `expected exactly one object matching node<version>-${LAMBDA_ARCH}.tar.gz under s3://${assetBucket}/${keyPrefix}, ` +
      `found ${candidates.length}${candidates.length ? `: ${candidates.join(", ")}` : ""}. Without the published cache ` +
      `there was nothing for the embed pass to embed — run assert-bytecode.mjs, which diagnoses the warm pass itself.`,
  );
}
const cacheKey = keyPrefix + candidates[0];
const entryName = embeddedBytecodePath(cacheKey);
if (!entryName) {
  fail(`could not derive an embedded tar path from ${cacheKey} — its name is not node<version>-<arch>.tar.gz`);
}
const taskPath = `${TASK_ROOT}/${entryName}`;
log(`the cache is published at s3://${assetBucket}/${cacheKey}, so the artifact must carry ${entryName}`);

const artifactPrefix = `${result.slug}/`;
const artifactKeys = await listRetrying(artifactBucket, artifactPrefix);
const pairs = embeddedArtifactPairs(artifactKeys);
if (pairs.length !== 1) {
  fail(
    `expected exactly one repackaged artifact (<hash>-bc-<digest>.zip) under s3://${artifactBucket}/${artifactPrefix}, ` +
      `found ${pairs.length}${pairs.length ? `: ${pairs.map((p) => p.embedded).join(", ")}` : ""}. None at all means the ` +
      `embed pass never uploaded one: it gates on the package's unzipped size and on the cache's, and a skip is a ` +
      `named warning line in the deploy output rather than a failure — check there first. Objects seen: ` +
      `${artifactKeys.join(", ") || "(none)"}`,
  );
}
const { embedded: embeddedKey, original: originalKey } = pairs[0];
if (!originalKey) {
  fail(
    `${embeddedKey} has no original counterpart under s3://${artifactBucket}/${artifactPrefix}, so there is nothing to ` +
      `compare the deployed code against — the artifact the deploy uploaded and warmed is gone`,
  );
}

const originalSha = shaBase64(readObject(artifactBucket, originalKey));
const fn = describeDeployedFunction(functionName);
const codeSha = fn.Configuration?.CodeSha256;
if (!codeSha) {
  fail(`lambda get-function reported no Configuration.CodeSha256 for ${functionName}`);
}
if (codeSha === originalSha) {
  fail(
    `${functionName} is still running the artifact the deploy originally uploaded (CodeSha256 ${codeSha} is the sha of ` +
      `s3://${artifactBucket}/${originalKey}), even though ${embeddedKey} exists. The repackaged bundle was uploaded ` +
      `but UpdateFunctionCode never moved the function onto it — check the deploy output for the embed pass's ` +
      `per-bundle lines.`,
  );
}
log(`${functionName} CodeSha256 ${codeSha} differs from the originally-uploaded ${originalKey} (${originalSha})`);

const location = fn.Code?.Location;
if (!location) {
  fail(`lambda get-function reported no Code.Location for ${functionName}, so its package cannot be inspected`);
}
const packageBytes = await downloadPackage(location);
const packageSha = shaBase64(packageBytes);
if (packageSha !== codeSha) {
  fail(
    `the package downloaded from ${functionName}'s Code.Location hashes to ${packageSha}, but the function reports ` +
      `CodeSha256 ${codeSha} — these are not the deployed bytes, so nothing read out of them says anything about the ` +
      `deployment. This is a fetch problem, not evidence the tar is missing.`,
  );
}

let entries;
try {
  entries = zipEntryNames(packageBytes);
} catch (err) {
  fail(
    `${functionName}'s deployment package (${packageBytes.length} bytes, CodeSha256 ${codeSha}) could not be read as a ` +
      `zip: ${err.message}`,
  );
}
if (!entries.includes(entryName)) {
  const near = entries.filter((name) => name.startsWith(".ocel/bytecode/"));
  fail(
    `${functionName}'s deployment package has ${entries.length} entries but not ${entryName}` +
      (near.length
        ? `. It does carry ${near.join(", ")} — an embedded cache under a name the membrane will not look for, which ` +
          `leaves every cold start silently falling back to S3.`
        : ` and nothing under .ocel/bytecode/ at all, so the function was moved onto a repackaged artifact that ` +
          `carries no cache.`),
  );
}
log(`the deployed package carries ${entryName} among its ${entries.length} entries`);

const burstStart = Date.now();
log(`bursting ${BURST_SIZE} concurrent requests to force fresh sandboxes`);
const burstResults = await Promise.all(
  Array.from({ length: BURST_SIZE }, (_, i) => {
    const burstTag = tagProbeTag(`embed-burst-${Date.now()}-${process.pid}-${i}`);
    const burstTarget = new URL(TAG_PROBE_ROUTE + `?tag=${encodeURIComponent(burstTag)}`, base).toString();
    return fetch(burstTarget, { method: "POST" })
      .then((r) => r.ok)
      .catch(() => false);
  }),
);
const burstSucceeded = burstResults.filter(Boolean).length;
log(`burst: ${burstSucceeded}/${BURST_SIZE} requests succeeded`);
if (burstSucceeded === 0) {
  fail(
    `all ${BURST_SIZE} burst requests to ${TAG_PROBE_ROUTE} failed — the function was never invoked, so no read leg of ` +
      `either kind could appear in its logs. This is a burst/deployment problem, not evidence the embedded path is ` +
      `broken.`,
  );
}

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
      `${(LOG_DEADLINE_MS + LOG_CONFIRM_DEADLINE_MS) / 1000}s (${coverage.detail}): ${logsError?.message}. Whatever was ` +
      `ingested after the last successful read was never looked at, so the absence of an S3 rehydrate line says ` +
      `nothing — it would be an absence of evidence rather than evidence of absence, and passing on it would report a ` +
      `working embedded path on a deployment that may have fallen through to S3 on every instance. ` +
      `${embeddedHits.length} embedded hit${embeddedHits.length === 1 ? "" : "s"} and ${s3Hits.length} S3 ` +
      `line${s3Hits.length === 1 ? "" : "s"} were seen in the part that was read.`,
  );
}
if (coverage.kind === "truncated") {
  fail(
    `the read of /aws/lambda/${functionName} covering the burst window is truncated (${coverage.detail}), so lines the ` +
      `burst produced are paged off the end of it and the absence of an S3 rehydrate line says nothing about the ` +
      `instances whose output did not fit. ${embeddedHits.length} embedded hit${embeddedHits.length === 1 ? "" : "s"} ` +
      `and ${s3Hits.length} S3 line${s3Hits.length === 1 ? "" : "s"} were seen in the page that was read.`,
  );
}
if (failures) {
  log(`read the burst window despite ${failures} failed attempt${failures === 1 ? "" : "s"}: ${coverage.detail}`);
}

if (s3Hits.length) {
  fail(
    `${s3Hits.length} instance${s3Hits.length === 1 ? "" : "s"} fetched the compile cache from S3 after the burst, ` +
      `which the embedded copy exists to make unnecessary — the local read was skipped or failed and fell through ` +
      `(${embedFailures.length} embedded-leg failure line${embedFailures.length === 1 ? "" : "s"}${
        embedFailures.length ? `: ${embedFailures.slice(0, 3).map((o) => o.message).join(" | ")}` : ""
      }). S3 lines: ${s3Hits.slice(0, 3).join(" | ")}`,
  );
}

if (embeddedHits.length === 0) {
  fail(
    `no instance reported loading the embedded compile cache from ${taskPath} within ${LOG_DEADLINE_MS / 1000}s of the ` +
      `burst, and none reported fetching from S3 either — ${summarizeOutcomes(embedFailures)} in ` +
      `/aws/lambda/${functionName}${
        embedFailures.length ? `; samples: ${embedFailures.slice(0, 5).map((o) => o.message).join(" | ")}` : ""
      }. Either every burst request landed on an instance that was already warm (no cold start runs a read leg at all), ` +
      `or the membrane on this deployment predates the embedded leg and never looks for the tar.`,
  );
}

log(`embedded hit: ${embeddedHits[0]}`);
log(
  `${embeddedHits.length} instance${embeddedHits.length === 1 ? "" : "s"} loaded the compile cache from the artifact, ` +
    `0 fetched it from S3 (${coverage.detail})`,
);
log("compile cache embedded in the deployment package and read from it end to end");

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
        fail(
          `could not list s3://${bucket}/${prefix} at all within ${LIST_RETRY_DEADLINE_MS / 1000}s — every attempt ` +
            `failed, so nothing here says what is there: ${err.message}`,
        );
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
  console.error(`[ocel-e2e] embed: ${message}`);
}

function fail(message) {
  console.error(`[ocel-e2e] embed assertion failed: ${message}`);
  process.exit(1);
}
