// The `aws` CLI calls the assertion scripts share (assert-bytecode.mjs,
// assert-embed.mjs).
//
// Deliberately not in lib.mjs: that module is the suite's pure logic, unit
// tested in lib.test.mjs. Nothing here can be, because every function shells out
// to a real account — so the two live apart rather than leaving lib.mjs half
// covered and its guarantee unclear.
//
// The low-level helpers throw on an AWS failure rather than swallow it: their
// callers are wait loops that need to tell a transient error (worth retrying,
// and silently) from a permanent one (worth failing on, loudly, once the loop
// gives up), and only the call site knows whether any prior attempt ever
// succeeded.
//
// The two resolvers at the bottom take the caller's `fail` for the mirror-image
// reason: what they enforce is an expectation (exactly one function, a bucket
// that exists) that no retry can change, and passing `fail` is what keeps the
// message under the calling script's own prefix instead of inventing a second
// reporting channel here.

import { execFileSync } from "node:child_process";

import { lambdaFunctionNames } from "./lib.mjs";

/** How long to wait between attempts at a list that could not be made. */
export const POLL_INTERVAL_MS = 3_000;

/** How long to keep retrying such a list before giving up on it entirely. */
export const LIST_RETRY_DEADLINE_MS = 30_000;

// CloudWatch Logs ingestion trails an invocation by anywhere from under a
// second to several — this is padded well past that for filter-log-events to
// see every burst instance's line.
export const LOG_POLL_INTERVAL_MS = 5_000;
export const LOG_DEADLINE_MS = 60_000;

// Deliberately well under LIST_RETRY_DEADLINE_MS/LOG_DEADLINE_MS: a single
// `aws` call runs inside a poll loop, and a timeout equal to the loop's own
// budget would let one wedged call consume the whole window and reduce the loop
// to a single attempt. This leaves room for several retries inside either
// deadline instead.
export const AWS_TIMEOUT_MS = 15_000;

/**
 * The page fetchFunctionLogs asks filter-log-events for. A response of exactly
 * this many events is a truncated read of the window, not a complete one — the
 * difference matters to any caller whose claim rests on a line being *absent*.
 */
export const LOG_PAGE_LIMIT = 1000;

// The membrane layer builds linux/amd64 only, which s3Arch
// (cloud/aws/cmd/lambdanode/bootstrap/bytecode.go) renders as x86_64 — the only
// spelling this suite's deploys can ever produce a key under.
export const LAMBDA_ARCH = "x86_64";

export function aws(args) {
  return execFileSync("aws", args, {
    encoding: "utf8",
    timeout: AWS_TIMEOUT_MS,
    stdio: ["ignore", "pipe", "pipe"],
    maxBuffer: 64 * 1024 * 1024,
  }).trim();
}

export function sleep(ms) {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

/** The full keys of every object under `prefix`, in the order S3 returned them. */
export function listObjectKeys(bucket, prefix) {
  const response = JSON.parse(
    aws(["s3api", "list-objects-v2", "--bucket", bucket, "--prefix", prefix, "--output", "json"]),
  );
  return (response.Contents ?? []).map((entry) => entry.Key);
}

// Mirrors logs.mjs's printLambdaLogs: same `aws logs filter-log-events` shape,
// but scoped to the one function resolveFunctionName already found rather than
// discovered again by tag — that resolution is already unambiguous to exactly
// one function. `filterPattern` narrows a window too wide to page through
// otherwise; a caller reading a window it has just created leaves it off and
// reads everything in it.
export function fetchFunctionLogs(functionName, startTime, filterPattern) {
  const response = JSON.parse(
    aws([
      "logs",
      "filter-log-events",
      "--log-group-name",
      `/aws/lambda/${functionName}`,
      "--start-time",
      String(startTime),
      ...(filterPattern ? ["--filter-pattern", filterPattern] : []),
      "--limit",
      String(LOG_PAGE_LIMIT),
      "--output",
      "json",
    ]),
  );
  return response.events ?? [];
}

export function getObject(bucket, key, maxBuffer = 128 * 1024 * 1024) {
  return execFileSync("aws", ["s3", "cp", `s3://${bucket}/${key}`, "-"], { maxBuffer });
}

export function describeFunction(functionName) {
  return JSON.parse(aws(["lambda", "get-function", "--function-name", functionName, "--output", "json"]));
}

// resolveFunctionName finds the one Lambda function an app deployed, the same
// way logs.mjs finds its log groups: by the ocel tags every Ocel function
// carries (cloud/aws/deploy/function.go). Both `ocel:project` and `ocel:app` are
// filtered on, unlike logs.mjs's project-only filter, because the keys these
// scripts compose are one function's — a project with more than one app would
// otherwise leave which function ambiguous.
export function resolveFunctionName(slug, app, fail) {
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

// resolveBootstrapBucket finds a substrate bucket the way it is provisioned
// (cloud/aws/bootstrap/bootstrap.go) rather than by guessing at a name. The
// asset bucket it resolves under `AssetBucket` is the same one the membrane is
// handed as OCEL_ISR_BUCKET, and the same one assert-tag-publisher.mjs looks up.
export function resolveBootstrapBucket(logicalId, envHint, fail) {
  const found = aws([
    "cloudformation",
    "describe-stack-resources",
    "--stack-name",
    process.env.OCEL_BOOTSTRAP_STACK || "ocel-bootstrap-preview",
    "--query",
    `StackResources[?LogicalResourceId==\`${logicalId}\`].PhysicalResourceId | [0]`,
    "--output",
    "text",
  ]);
  if (!found || found === "None") {
    fail(`could not resolve the substrate's ${logicalId}; set ${envHint}`);
  }
  return found;
}
