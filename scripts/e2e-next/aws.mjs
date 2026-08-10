// The `aws` CLI calls the assertion scripts share (assert-bytecode.mjs,
// assert-embed.mjs) and the one sweep-projects.mjs makes.
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

import { envSegment, lambdaFunctionNames } from "./lib.mjs";

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

// One more in-process attempt than the CLI's default of three, which is what a
// throttled call spends before it ever reaches a caller's poll loop. It cannot
// go much higher: every attempt after the first waits an exponentially growing
// backoff, and the whole ladder has to finish inside AWS_TIMEOUT_MS or the
// process is killed mid-retry. Adaptive mode would be the better policy and is
// not available here — its rate limiter learns across calls, and each `aws` is
// a fresh process that remembers nothing.
//
// Exported because not every `aws` this suite runs can go through the wrapper
// below — a binary read wants no encoding, and the harness scripts spawn with
// their own timeouts — and a call left off this env is back on the CLI default
// with nothing to say so. Spread it into `env` alongside process.env; that is
// the whole contract.
export const AWS_CLI_RETRY_ENV = Object.freeze({ AWS_RETRY_MODE: "standard", AWS_MAX_ATTEMPTS: "4" });

export function aws(args) {
  return execFileSync("aws", args, {
    encoding: "utf8",
    timeout: AWS_TIMEOUT_MS,
    stdio: ["ignore", "pipe", "pipe"],
    maxBuffer: 64 * 1024 * 1024,
    env: { ...process.env, ...AWS_CLI_RETRY_ENV },
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

/**
 * The names of every SSM parameter under a path. `describe-parameters` rather
 * than `get-parameters-by-path`: the only thing any caller here wants is the
 * names, and these parameters are SecureStrings holding a project's root-stack
 * secret and owner token — reading their values to learn their names would be
 * asking for something this suite has no business handling.
 */
export function listParameterNames(pathPrefix) {
  const response = JSON.parse(
    aws([
      "ssm",
      "describe-parameters",
      "--parameter-filters",
      `Key=Name,Option=BeginsWith,Values=${pathPrefix}`,
      "--output",
      "json",
    ]),
  );
  return (response.Parameters ?? []).map((entry) => entry.Name);
}

// Not routed through aws() above: this one hands back the raw bytes (bytecode
// archives, compressed assets), and the wrapper decodes as utf8 and trims,
// which would corrupt them. It still spawns under AWS_CLI_RETRY_ENV — a
// throttled read here is no less transient than a throttled list.
export function getObject(bucket, key, maxBuffer = 128 * 1024 * 1024) {
  return execFileSync("aws", ["s3", "cp", `s3://${bucket}/${key}`, "-"], {
    maxBuffer,
    env: { ...process.env, ...AWS_CLI_RETRY_ENV },
  });
}

export function describeFunction(functionName) {
  return JSON.parse(aws(["lambda", "get-function", "--function-name", functionName, "--output", "json"]));
}

// resolveFunctionName finds the one Lambda function an app deployed, the same
// way logs.mjs finds its log groups: by the ocel tags every Ocel function
// carries (cloud/aws/deploy/function.go). All three are filtered on, because
// the keys these scripts compose are one deployment's: a whole CI run shares
// one project, so `ocel:project` alone matches every fixture's function, and
// every one of them declares the same app name. `ocel:env` — the asset key's
// environment segment, "preview-<pointer>" — is what separates them.
export function resolveFunctionName(slug, app, environment, fail) {
  const env = envSegment(environment);
  const names = lambdaFunctionNames(
    JSON.parse(
      aws([
        "resourcegroupstaggingapi",
        "get-resources",
        "--tag-filters",
        `Key=ocel:project,Values=${slug}`,
        `Key=ocel:app,Values=${app}`,
        `Key=ocel:env,Values=${env}`,
        "--resource-type-filters",
        "lambda:function",
        "--output",
        "json",
      ]),
    ),
  );
  if (names.length !== 1) {
    fail(
      `expected exactly one lambda function tagged ocel:project=${slug} ocel:app=${app} ocel:env=${env}, found ` +
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
