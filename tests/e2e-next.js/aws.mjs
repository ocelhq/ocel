import { execFileSync } from "node:child_process";

import { envSegment, lambdaFunctionNames } from "./lib.mjs";

export const POLL_INTERVAL_MS = 3_000;

export const LIST_RETRY_DEADLINE_MS = 30_000;

export const LOG_POLL_INTERVAL_MS = 5_000;
export const LOG_DEADLINE_MS = 60_000;

export const AWS_TIMEOUT_MS = 15_000;

export const LOG_PAGE_LIMIT = 1000;

export const LAMBDA_ARCH = "x86_64";

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

export function listObjectKeys(bucket, prefix) {
  const response = JSON.parse(
    aws(["s3api", "list-objects-v2", "--bucket", bucket, "--prefix", prefix, "--output", "json"]),
  );
  return (response.Contents ?? []).map((entry) => entry.Key);
}

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

export function getObject(bucket, key, maxBuffer = 128 * 1024 * 1024) {
  return execFileSync("aws", ["s3", "cp", `s3://${bucket}/${key}`, "-"], {
    maxBuffer,
    env: { ...process.env, ...AWS_CLI_RETRY_ENV },
  });
}

export function describeFunction(functionName) {
  return JSON.parse(aws(["lambda", "get-function", "--function-name", functionName, "--output", "json"]));
}

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
