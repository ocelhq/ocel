#!/usr/bin/env node

import { existsSync, readFileSync } from "node:fs";
import { join } from "node:path";

import {
  DEPLOY_RESULT_FILE,
  ECHO_PROBE_HEADER,
  HEALTH_ROUTE,
  MARKER_ROUTE,
  STATE_FILE,
  echoMismatches,
  echoRequest,
  edgeVerdict,
  isFunctionURLHost,
  previewRefForApp,
  projectSlugForRun,
  resolveAppURLs,
  serveMarker,
  statusRoute,
  urlHost,
} from "./lib.mjs";
import { awsUnreachable, functionURLConfig, resolveFunctionName } from "./aws.mjs";

const READY_DEADLINE_MS = Number(process.env.OCEL_E2E_NODE_READY_MS) || 180_000;
const READY_INTERVAL_MS = 5_000;

const STATUS_PROBE = 418;

const appDir = process.cwd();

const failures = [];
const skips = [];

const result = readDeployResult();
const { slug, ref } = readIdentity();
const { resolved, unattributed } = resolveAppURLs(result, { slug, pointer: ref });

for (const url of unattributed) {
  fail(
    `${url} is in appUrls but matches no app's preview hostname` +
      (isFunctionURLHost(urlHost(url))
        ? " — it is a Lambda Function URL, which the deploy only falls back to when an app got no edge worker"
        : ""),
  );
}

for (const entry of resolved) {
  await checkApp(entry);
}

await checkFunctionURLsAreClosed();

report();

async function checkApp({ app, framework, url }) {
  if (!url) {
    fail(
      `app ${app} (${framework}) has no edge URL in ${DEPLOY_RESULT_FILE}: appUrls is ` +
        `${JSON.stringify(result.appUrls)}. An app served from the edge is announced at its preview ` +
        `hostname; a missing one means no worker was created for it.`,
    );
    return;
  }

  const first = await waitForServe(url, framework);
  if (!first) return;

  const verdict = edgeVerdict({ url, status: first.status, headers: first.headers });
  if (verdict.kind !== "edge") {
    fail(`${app} (${framework}): ${verdict.detail}`);
    return;
  }
  log(`${app} (${framework}): ${verdict.detail}`);

  const marker = serveMarker(framework);
  if (!first.body.includes(marker)) {
    fail(
      `${app} (${framework}): ${new URL(MARKER_ROUTE, url)} answered ${first.status} but its body does not ` +
        `contain ${marker}; the bytes did not come from the deployed app:\n${first.body.slice(0, 400)}`,
    );
    return;
  }
  log(`${app} (${framework}): ${MARKER_ROUTE} served ${marker} through the worker`);

  await checkHealth(app, framework, url);
  await checkEcho(app, framework, url);
  await checkStatusPassthrough(app, framework, url);
}

async function waitForServe(url, framework) {
  const target = new URL(MARKER_ROUTE, url);
  const deadline = Date.now() + READY_DEADLINE_MS;
  let last = "never answered";
  for (;;) {
    try {
      const response = await fetch(target, { redirect: "manual" });
      const body = await response.text();
      if (response.status === 200) {
        return { status: response.status, headers: response.headers, body };
      }
      last = `status ${response.status}: ${body.slice(0, 200)}`;
      if (response.status === 403) {
        last += " (a 403 is what an IAM-authed Lambda Function URL answers an unsigned request with)";
      }
    } catch (err) {
      last = err.message;
    }
    if (Date.now() >= deadline) {
      fail(
        `${framework}: ${target} never served a 200 within ${Math.round(READY_DEADLINE_MS / 1000)}s; ` +
          `last attempt: ${last}`,
      );
      return null;
    }
    await sleep(READY_INTERVAL_MS);
  }
}

async function checkHealth(app, framework, url) {
  const target = new URL(HEALTH_ROUTE, url);
  const response = await fetch(target);
  const body = await response.text();
  let parsed;
  try {
    parsed = JSON.parse(body);
  } catch {
    parsed = null;
  }
  if (response.status !== 200 || parsed?.ok !== true || parsed?.framework !== framework) {
    fail(`${app} (${framework}): ${target} answered ${response.status} ${body.slice(0, 200)}`);
    return;
  }
  log(`${app} (${framework}): ${HEALTH_ROUTE} answered a JSON body naming ${framework}`);
}

async function checkEcho(app, framework, url) {
  const stamp = `${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 8)}`;
  const sent = { framework, ...echoRequest(stamp) };
  const target = new URL(sent.path, url);
  for (const [name, value] of Object.entries(sent.query)) {
    target.searchParams.set(name, value);
  }

  const response = await fetch(target, {
    method: "POST",
    headers: { "content-type": "application/json", [ECHO_PROBE_HEADER]: sent.header.value },
    body: JSON.stringify(sent.body),
  });
  const body = await response.text();
  if (response.status !== 200) {
    fail(`${app} (${framework}): ${target} answered ${response.status} ${body.slice(0, 300)}`);
    return;
  }

  let echo;
  try {
    echo = JSON.parse(body);
  } catch {
    fail(`${app} (${framework}): ${target} answered 200 with a body that is not JSON:\n${body.slice(0, 300)}`);
    return;
  }

  const problems = echoMismatches(sent, echo);
  if (problems.length > 0) {
    fail(`${app} (${framework}): the worker did not carry the request through intact:\n  ${problems.join("\n  ")}`);
    return;
  }
  log(`${app} (${framework}): method, deep path, query, header and JSON body all reached the app intact`);
}

async function checkStatusPassthrough(app, framework, url) {
  const target = new URL(statusRoute(STATUS_PROBE), url);
  const response = await fetch(target, { redirect: "manual" });
  await response.text();
  if (response.status !== STATUS_PROBE) {
    fail(
      `${app} (${framework}): ${target} answered ${response.status}, not the ${STATUS_PROBE} the app set — ` +
        `the worker rewrote the origin's status`,
    );
    return;
  }
  log(`${app} (${framework}): the app's ${STATUS_PROBE} reached the client unchanged`);
}

async function checkFunctionURLsAreClosed() {
  const served = resolved.filter((entry) => entry.url);
  if (served.length === 0) {
    return;
  }

  const unreachable = awsUnreachable();
  if (unreachable) {
    skip(
      `nothing checked that the origins are closed: AWS is not reachable from here (${unreachable}). ` +
        `The edge assertions above still stand, but without this leg nothing here rules out an origin ` +
        `that answers unsigned requests directly. Re-run with the aws CLI and credentials.`,
    );
    return;
  }

  for (const entry of served) {
    const functionName = resolveFunctionName(slug, entry.app, result.environment, fail);
    if (!functionName) {
      continue;
    }
    const config = functionURLConfig(functionName);
    if (!config) {
      log(`${entry.app} (${entry.framework}): ${functionName} publishes no Function URL at all`);
      continue;
    }
    if (config.AuthType !== "AWS_IAM") {
      fail(
        `${entry.app} (${entry.framework}): ${functionName} publishes a Function URL with AuthType ` +
          `${config.AuthType}, so the origin is reachable without the worker's signature`,
      );
      continue;
    }
    const response = await fetch(config.FunctionUrl).catch((err) => ({ status: `fetch failed: ${err.message}` }));
    if (response.status !== 403) {
      fail(
        `${entry.app} (${entry.framework}): an unsigned request to ${config.FunctionUrl} answered ` +
          `${response.status}; an AWS_IAM Function URL must answer 403, or the 200 through the edge ` +
          `proves nothing about the worker`,
      );
      continue;
    }
    log(
      `${entry.app} (${entry.framework}): ${functionName}'s Function URL 403s unsigned, so the 200 above ` +
        `could only have come through the worker`,
    );
  }
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

function sleep(ms) {
  return new Promise((r) => setTimeout(r, ms));
}

function log(message) {
  process.stdout.write(`[ocel-e2e-node] serves: ${message}\n`);
}

function skip(message) {
  skips.push(message);
  process.stdout.write(`[ocel-e2e-node] serves: SKIPPED ${message}\n`);
}

function fail(message) {
  failures.push(message);
  process.stderr.write(`[ocel-e2e-node] serves: FAILED ${message}\n`);
}

function die(message) {
  process.stderr.write(`[ocel-e2e-node] serves: ${message}\n`);
  process.exit(2);
}

function report() {
  if (failures.length > 0) {
    process.stderr.write(`[ocel-e2e-node] serves: ${failures.length} assertion(s) failed\n`);
    process.exit(1);
  }
  process.stdout.write(
    `[ocel-e2e-node] serves: every app answered a real request through the edge worker` +
      `${skips.length > 0 ? `, with ${skips.length} leg(s) unverified` : ""}\n`,
  );
}
