#!/usr/bin/env node
// Asserts that a Next app's V8 compile cache is actually *published* to S3 on
// a live deployment — not merely that the deploy succeeded and a request
// answers 200.
//
// Why this exists as its own assertion: everything between a warm compile
// cache in /tmp and a reusable object in S3 is machinery no request can see —
// the flush-compile-cache control message, the membrane's once-per-instance
// tar+gzip+upload, and the HEAD guard that skips a redundant re-upload
// (cloud/aws/cmd/lambdanode/bootstrap/bytecode.go). A 200 from the deployment
// proves none of it; only an object at the exact key the membrane composes
// does. Nothing reads the archive back yet — rehydration is a later step —
// so this only proves the write leg.
//
// Usage: assert-bytecode.mjs [deployment-url]
//   falls back to $NEXT_TEST_DEPLOY_URL, then $SMOKE_URL.
//   Run from the deployed app's directory: slug, environment, app name and
//   build id are read from .ocel/deploy-result.json there — the same
//   document logs.mjs reads — rather than asked for by hand.
//   $OCEL_ASSET_BUCKET names the bucket the membrane uploads to; without it
//   the script resolves the substrate's from the preview bootstrap stack.
//
// Exits non-zero with the observations it collected.

import { execFileSync } from "node:child_process";
import { existsSync, readFileSync } from "node:fs";
import { join } from "node:path";
import { gunzipSync } from "node:zlib";

import {
  DEPLOY_RESULT_FILE,
  LAMBDA_RUNTIME,
  TAG_PROBE_ROUTE,
  appAssetPrefix,
  bytecodeCacheKey,
  lambdaFunctionNames,
  nodeMajorFromRuntime,
  tagProbeTag,
  tarEntryNames,
} from "./lib.mjs";

// The membrane's upload runs off the request path after an invocation
// completes, itself bounded by a 2s budget (bytecodeUploadBudget,
// cloud/aws/cmd/lambdanode/bootstrap/bytecode.go) — but that budget starts
// only once the instance gets around to running it, so the wait here is
// padded well past it for a cold start plus S3 round trips.
const POLL_INTERVAL_MS = 3_000;
const UPLOAD_DEADLINE_MS = 60_000;

// The membrane layer builds linux/amd64 only, which s3Arch
// (cloud/aws/cmd/lambdanode/bootstrap/bytecode.go) renders as x86_64 — the
// only spelling this suite's deploys can ever produce a key under.
const LAMBDA_ARCH = "x86_64";

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

const bucket = process.env.OCEL_ASSET_BUCKET || resolveAssetBucket();
const prefix = appAssetPrefix({
  environment: result.environment,
  slug: result.slug,
  app: app.name,
  buildId: app.buildId,
});
const functionName = resolveFunctionName(result.slug, app.name);
const key = bytecodeCacheKey({
  prefix,
  functionName,
  nodeMajor: nodeMajorFromRuntime(LAMBDA_RUNTIME),
  arch: LAMBDA_ARCH,
});
log(`expecting s3://${bucket}/${key}`);

// TAG_PROBE_ROUTE is force-dynamic (see its own file), so this is guaranteed
// to reach the Lambda rather than being answered from an edge- or CDN-cached
// response — reusing it borrows a route already proven to invoke the
// function rather than adding a new one just for this.
const tag = tagProbeTag(`bytecode-${Date.now()}-${process.pid}`);
const target = new URL(TAG_PROBE_ROUTE + `?tag=${encodeURIComponent(tag)}`, base).toString();
log(`invoking ${target} to force an instance to run`);
const response = await fetch(target, { method: "POST" });
if (!response.ok) {
  fail(`${target} answered ${response.status}; never invoked the function at all`);
}

log(`polling for up to ${UPLOAD_DEADLINE_MS / 1000}s`);
const deadline = Date.now() + UPLOAD_DEADLINE_MS;
let found = false;
while (Date.now() < deadline) {
  if (objectExists(bucket, key)) {
    found = true;
    break;
  }
  await sleep(POLL_INTERVAL_MS);
}
if (!found) {
  fail(
    `s3://${bucket}/${key} never appeared within ${UPLOAD_DEADLINE_MS / 1000}s of invoking ${target}. ` +
      `The invocation succeeded, so either the instance it landed on had already uploaded (the HEAD ` +
      `guard means only the first instance to finish ever does, and this key is meant to be stable ` +
      `across requests) or the upload path is broken — check the function's CloudWatch logs for ` +
      `"skipping upload" / "skipping compile cache upload".`,
  );
}

const body = getObject(bucket, key);
let archive;
try {
  archive = gunzipSync(body);
} catch (err) {
  fail(`s3://${bucket}/${key} (${body.length} bytes) is not valid gzip: ${err.message}`);
}

let names;
try {
  names = tarEntryNames(archive);
} catch (err) {
  fail(`s3://${bucket}/${key} decompresses to ${archive.length} bytes that are not a valid tar: ${err.message}`);
}
if (names.length === 0) {
  fail(`s3://${bucket}/${key} decompresses to an empty tar — no compile cache was actually archived`);
}
if (!names.some((name) => name.includes("/"))) {
  fail(
    `s3://${bucket}/${key} has no entry under a subdirectory (${names.join(", ")}) — node nests its ` +
      `compile cache under a version-hash directory, so a flat archive means nothing real was cached`,
  );
}

log(
  `s3://${bucket}/${key}: valid gzip+tar, ${names.length} entr${names.length === 1 ? "y" : "ies"}, ` +
    `nested under a subdirectory`,
);
log("bytecode cache published end to end");

// --- AWS -------------------------------------------------------------------

function objectExists(bucket, key) {
  try {
    execFileSync("aws", ["s3api", "head-object", "--bucket", bucket, "--key", key], {
      stdio: ["ignore", "pipe", "pipe"],
    });
    return true;
  } catch {
    return false;
  }
}

function getObject(bucket, key) {
  return execFileSync("aws", ["s3", "cp", `s3://${bucket}/${key}`, "-"], {
    maxBuffer: 128 * 1024 * 1024,
  });
}

// resolveFunctionName finds the one Lambda function this app deployed, the
// same way logs.mjs finds its log groups: by the ocel tags every Ocel
// function carries (cloud/aws/deploy/function.go). Both `ocel:project` and
// `ocel:app` are filtered on, unlike logs.mjs's project-only filter, because
// the key this script composes is one function's — a project with more than
// one app would otherwise leave which function ambiguous.
function resolveFunctionName(slug, app) {
  const names = lambdaFunctionNames(
    JSON.parse(
      aws([
        "resourcegroupstaggingapi",
        "get-resources",
        "--tag-filters",
        `Key=ocel:project,Values=${slug}`,
        `Key=ocel:app,Values=${app}`,
        "--resource-type-filters",
        "lambda:function",
        "--output",
        "json",
      ]),
    ),
  );
  if (names.length !== 1) {
    fail(
      `expected exactly one lambda function tagged ocel:project=${slug} ocel:app=${app}, found ` +
        `${names.length}${names.length ? `: ${names.join(", ")}` : ""}`,
    );
  }
  return names[0];
}

// resolveAssetBucket finds the substrate's asset bucket the same way the
// membrane is given it (OCEL_ISR_BUCKET, itself cfg.AssetBucket): from the
// preview bootstrap stack, rather than by guessing at a name. Mirrors
// assert-tag-publisher.mjs's helper of the same name — it is the same bucket.
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
  return execFileSync("aws", args, { encoding: "utf8", maxBuffer: 64 * 1024 * 1024 }).trim();
}

function sleep(ms) {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

function log(message) {
  console.error(`[ocel-e2e] bytecode: ${message}`);
}

function fail(message) {
  console.error(`[ocel-e2e] bytecode assertion failed: ${message}`);
  process.exit(1);
}
