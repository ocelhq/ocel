#!/usr/bin/env node

import { existsSync, readFileSync } from "node:fs";
import { join } from "node:path";

import {
  BYTECODE_BUCKET_ENV,
  BYTECODE_CACHE_ENV,
  BYTECODE_PREFIX_ENV,
  DEPLOY_RESULT_FILE,
  HEALTH_ROUTE,
  STATE_FILE,
  bytecodeArchiveName,
  bytecodePrefixProblem,
  bytecodeRehydrateOutcome,
  bytecodeSettings,
  previewRefForApp,
  projectSlugForRun,
  resolveAppURLs,
  summarizeOutcomes,
} from "./lib.mjs";
import {
  LIST_RETRY_DEADLINE_MS,
  LOG_DEADLINE_MS,
  LOG_POLL_INTERVAL_MS,
  POLL_INTERVAL_MS,
  awsUnreachable,
  fetchFunctionLogs,
  functionEnvironment,
  listObjectKeys,
  resolveFunctionName,
  sleep,
} from "./aws.mjs";

const BURST_SIZE = Number(process.env.OCEL_E2E_NODE_BURST) || 20;

const LOG_CLOCK_SKEW_MS = 30_000;

const LOG_FILTER = `"compile cache"`;

const appDir = process.cwd();

const failures = [];
const skips = [];

const unreachable = awsUnreachable();
if (unreachable) {
  die(
    `this assertion is entirely AWS-side and AWS is not reachable from here (${unreachable}); ` +
      `it needs the aws CLI and the disposable account's credentials`,
  );
}

const result = readDeployResult();
const { slug, ref } = readIdentity();
const { resolved } = resolveAppURLs(result, { slug, pointer: ref });

for (const entry of resolved) {
  await checkApp(entry);
}

report();

async function checkApp({ app, framework, url }) {
  if (!url) {
    fail(
      `${app} (${framework}) has no edge URL in ${DEPLOY_RESULT_FILE}, so there is no way to force a cold start ` +
        `through the worker; assert-serves.mjs names why an app gets no URL`,
    );
    return;
  }

  const functionName = resolveFunctionName(slug, app, result.environment, fail);
  if (!functionName) return;

  const settings = bytecodeSettings(functionEnvironment(functionName));
  if (settings.kind === "absent") {
    fail(
      `${app} (${framework}): ${functionName} carries neither ${BYTECODE_BUCKET_ENV} nor ${BYTECODE_PREFIX_ENV}, so ` +
        `its node child is never given NODE_COMPILE_CACHE and no compile cache exists to write or read. Either the ` +
        `deploy did not set ${BYTECODE_CACHE_ENV}=1, or the deploy path still hands the bytecode coordinate only to ` +
        `next apps — which is the silence this suite exists to break.`,
    );
    return;
  }
  if (settings.kind === "partial") {
    fail(
      `${app} (${framework}): ${functionName} carries only half the bytecode coordinate — ${settings.missing.join(", ")} ` +
        `is missing, so the bootstrap resolves no cache at all and says nothing about it`,
    );
    return;
  }
  log(`${app} (${framework}): ${functionName} points at s3://${settings.bucket}/${settings.prefix}/`);

  const problem = bytecodePrefixProblem({ prefix: settings.prefix, environment: result.environment, slug, app });
  if (problem) {
    skip(
      `${app} (${framework}): the prefix on the function is not this app's bytecode coordinate (${problem}). ` +
        `The legs below still ran, against whatever the function actually names.`,
    );
  }

  const key = await findCacheObject({ app, framework, functionName, ...settings });
  if (!key) return;
  log(
    `${app} (${framework}): s3://${settings.bucket}/${key} already exists, before this script has issued a request — ` +
      `the deploy's warm pass wrote it`,
  );

  await checkRehydrate({ app, framework, url, functionName, key, bucket: settings.bucket });
}

async function findCacheObject({ app, framework, functionName, bucket, prefix }) {
  const listPrefix = `${prefix}/`;
  let keys = null;
  let listError = null;
  const deadline = Date.now() + LIST_RETRY_DEADLINE_MS;
  while (keys === null) {
    try {
      keys = listObjectKeys(bucket, listPrefix);
    } catch (err) {
      listError = err;
      if (Date.now() >= deadline) {
        fail(
          `${app} (${framework}): could not list s3://${bucket}/${listPrefix} at all within ` +
            `${LIST_RETRY_DEADLINE_MS / 1000}s, so nothing here says whether the cache was ever written: ` +
            `${listError.message}`,
        );
        return null;
      }
      log(`${app} (${framework}): could not list s3://${bucket}/${listPrefix} (${err.message}); will retry`);
      await sleep(POLL_INTERVAL_MS);
    }
  }

  const archives = keys.filter((key) => bytecodeArchiveName(key));
  if (archives.length === 0) {
    fail(
      `${app} (${framework}): nothing matching node<version>-<arch>.tar.gz exists under s3://${bucket}/${listPrefix}` +
        `${keys.length ? ` (it holds ${keys.length} other object(s): ${keys.slice(0, 5).join(", ")})` : " (it is empty)"}` +
        `, and this script has not touched the deployment yet. The function was given the coordinate, so the flag was ` +
        `on and the write leg is what failed: check the deploy output for its "warmed N/M bundles" lines and ` +
        `/aws/lambda/${functionName} for "ocel: warm invocation:".`,
    );
    return null;
  }
  if (archives.length > 1) {
    fail(
      `${app} (${framework}): ${archives.length} cache archives exist under s3://${bucket}/${listPrefix} ` +
        `(${archives.join(", ")}); one release of one function writes exactly one`,
    );
    return null;
  }
  return archives[0];
}

async function checkRehydrate({ app, framework, url, functionName, key, bucket }) {
  const burstStart = Date.now() - LOG_CLOCK_SKEW_MS;
  log(`${app} (${framework}): bursting ${BURST_SIZE} concurrent requests to force fresh sandboxes`);
  const answered = await Promise.all(
    Array.from({ length: BURST_SIZE }, (_, i) => {
      const target = new URL(HEALTH_ROUTE, url);
      target.searchParams.set("cold", `${Date.now().toString(36)}-${process.pid}-${i}`);
      return fetch(target, { headers: { "cache-control": "no-cache" } })
        .then((response) => response.ok)
        .catch(() => false);
    }),
  );
  const succeeded = answered.filter(Boolean).length;
  if (succeeded === 0) {
    fail(
      `${app} (${framework}): all ${BURST_SIZE} burst requests to ${HEALTH_ROUTE} failed, so the function was never ` +
        `invoked and no cold start could report anything. That is a deployment problem, not evidence about the ` +
        `read leg — assert-serves.mjs is the assertion for it.`,
    );
    return;
  }
  log(`${app} (${framework}): ${succeeded}/${BURST_SIZE} burst requests answered`);

  log(`${app} (${framework}): polling /aws/lambda/${functionName} for up to ${LOG_DEADLINE_MS / 1000}s`);
  const deadline = Date.now() + LOG_DEADLINE_MS;
  const observed = [];
  const seen = new Set();
  let hit = null;
  let logsRead = false;
  let logsError = null;
  while (Date.now() < deadline && !hit) {
    let events;
    try {
      events = fetchFunctionLogs(functionName, burstStart, LOG_FILTER);
      logsRead = true;
    } catch (err) {
      logsError = err;
      log(`${app} (${framework}): could not read /aws/lambda/${functionName} (${err.message}); will retry`);
      await sleep(LOG_POLL_INTERVAL_MS);
      continue;
    }
    for (const event of events) {
      if (event.eventId) {
        if (seen.has(event.eventId)) continue;
        seen.add(event.eventId);
      }
      const outcome = bytecodeRehydrateOutcome(event.message, key);
      if (!outcome) continue;
      if (outcome.kind === "hit") {
        hit = outcome;
        break;
      }
      observed.push(outcome);
    }
    if (!hit) await sleep(LOG_POLL_INTERVAL_MS);
  }

  if (hit) {
    log(`${app} (${framework}): a cold start read the cache back — ${hit.message.trim()}`);
    return;
  }
  if (!logsRead) {
    fail(
      `${app} (${framework}): could not read /aws/lambda/${functionName} at all within ${LOG_DEADLINE_MS / 1000}s, so ` +
        `nothing here says whether s3://${bucket}/${key} was ever read back: ${logsError?.message}`,
    );
    return;
  }
  if (observed.some((outcome) => outcome.kind === "embedded")) {
    skip(
      `${app} (${framework}): the read leg is UNPROVEN, not passed — the cold starts loaded an embedded compile cache ` +
        `from the function's own artifact, so no instance will ever report reading s3://${bucket}/${key}. Redeploy ` +
        `without the embed to exercise the S3 read leg.`,
    );
    return;
  }
  const samples = observed.slice(0, 5).map((outcome) => outcome.message.trim());
  fail(
    `${app} (${framework}): s3://${bucket}/${key} was written but no cold start reported reading it within ` +
      `${LOG_DEADLINE_MS / 1000}s of the burst (${summarizeOutcomes(observed)} in /aws/lambda/${functionName})` +
      `${samples.length ? `:\n  ${samples.join("\n  ")}` : ""}`,
  );
}

function readDeployResult() {
  const path = join(appDir, DEPLOY_RESULT_FILE);
  if (!existsSync(path)) {
    die(`no ${DEPLOY_RESULT_FILE} in ${appDir}; run deploy.mjs from the staged app directory first`);
  }
  return JSON.parse(readFileSync(path, "utf8"));
}

function readIdentity() {
  try {
    const state = JSON.parse(readFileSync(join(appDir, STATE_FILE), "utf8"));
    if (state?.slug && state?.ref) return { slug: state.slug, ref: state.ref };
  } catch {
    console.error(`[ocel-e2e-node] no readable ${STATE_FILE}; re-deriving the project slug and preview ref`);
  }
  return { slug: projectSlugForRun(), ref: previewRefForApp(appDir) };
}

function log(message) {
  process.stdout.write(`[ocel-e2e-node] bytecode: ${message}\n`);
}

function skip(message) {
  skips.push(message);
  process.stdout.write(`[ocel-e2e-node] bytecode: SKIPPED ${message}\n`);
}

function fail(message) {
  failures.push(message);
  process.stderr.write(`[ocel-e2e-node] bytecode: FAILED ${message}\n`);
}

function die(message) {
  process.stderr.write(`[ocel-e2e-node] bytecode: ${message}\n`);
  process.exit(2);
}

function report() {
  if (failures.length > 0) {
    process.stderr.write(`[ocel-e2e-node] bytecode: ${failures.length} assertion(s) failed\n`);
    process.exit(1);
  }
  process.stdout.write(
    `[ocel-e2e-node] bytecode: every app published a compile cache and read it back on a cold start` +
      `${skips.length > 0 ? `, with ${skips.length} leg(s) unverified` : ""}\n`,
  );
}
