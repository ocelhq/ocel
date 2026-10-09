import { execFileSync } from "node:child_process";

import { DEFAULT_NAMESPACE, lambdaFunctionNames } from "./lib.mjs";

export const POLL_INTERVAL_MS = 3_000;

export const LIST_RETRY_DEADLINE_MS = 30_000;

export const LOG_POLL_INTERVAL_MS = 5_000;
export const LOG_DEADLINE_MS = 60_000;

export const AWS_TIMEOUT_MS = 15_000;

export const LOG_PAGE_LIMIT = 1000;

export const LAMBDA_ARCH = "x86_64";

export const AWS_CLI_RETRY_ENV = Object.freeze({
  AWS_RETRY_MODE: "standard",
  AWS_MAX_ATTEMPTS: "4",
});

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

const logGroupByFunction = new Map();

export function functionLogGroup(functionName) {
  const cached = logGroupByFunction.get(functionName);
  if (cached) {
    return cached;
  }
  const found = aws([
    "lambda",
    "get-function-configuration",
    "--function-name",
    functionName,
    "--query",
    "LoggingConfig.LogGroup",
    "--output",
    "text",
  ]);
  if (!found || found === "None") {
    throw new Error(`lambda ${functionName} reports no LoggingConfig.LogGroup`);
  }
  logGroupByFunction.set(functionName, found);
  return found;
}

export function functionLogGroupLabel(functionName) {
  try {
    return functionLogGroup(functionName);
  } catch {
    return `the log group of ${functionName}`;
  }
}

export function fetchFunctionLogs(functionName, startTime, filterPattern) {
  const response = JSON.parse(
    aws([
      "logs",
      "filter-log-events",
      "--log-group-name",
      functionLogGroup(functionName),
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

export function previewStateTable() {
  const stack = previewBootstrapStack();
  let name;
  try {
    name = aws([
      "cloudformation",
      "describe-stacks",
      "--stack-name",
      stack,
      "--query",
      "Stacks[0].Outputs[?OutputKey=='StateTableName']|[0].OutputValue",
      "--output",
      "text",
    ]);
  } catch (err) {
    if (/does not exist/i.test(String(err.stderr ?? err.message))) {
      return undefined;
    }
    throw err;
  }
  if (!name || name === "None") {
    throw new Error(`the ${stack} stack publishes no StateTableName output`);
  }
  return name;
}

export function queryStatePartition(table, pk) {
  const items = [];
  let start;
  do {
    const page = JSON.parse(
      aws([
        "dynamodb",
        "query",
        "--table-name",
        table,
        "--consistent-read",
        "--key-condition-expression",
        "pk = :pk",
        "--expression-attribute-values",
        JSON.stringify({ ":pk": { S: pk } }),
        ...(start ? ["--starting-token", start] : []),
        "--output",
        "json",
      ]),
    );
    items.push(...(page.Items ?? []));
    start = page.NextToken;
  } while (start);
  return items;
}

export function getObject(bucket, key, maxBuffer = 128 * 1024 * 1024) {
  return execFileSync("aws", ["s3", "cp", `s3://${bucket}/${key}`, "-"], {
    maxBuffer,
    env: { ...process.env, ...AWS_CLI_RETRY_ENV },
  });
}

export function describeFunction(functionName) {
  return JSON.parse(
    aws(["lambda", "get-function", "--function-name", functionName, "--output", "json"]),
  );
}

export function resolveFunctionName({ project, app, env, release }, fail) {
  const tags = `ocel:project=${project} ocel:app=${app} ocel:env=${env} ocel:release=${release}`;
  const names = lambdaFunctionNames(
    JSON.parse(
      aws([
        "resourcegroupstaggingapi",
        "get-resources",
        "--tag-filters",
        `Key=ocel:project,Values=${project}`,
        `Key=ocel:app,Values=${app}`,
        `Key=ocel:env,Values=${env}`,
        `Key=ocel:release,Values=${release}`,
        "--resource-type-filters",
        "lambda:function",
        "--output",
        "json",
      ]),
    ),
  );
  if (names.length !== 1) {
    fail(
      `expected exactly one lambda function tagged ${tags}, found ` +
        `${names.length}${names.length ? `: ${names.join(", ")}` : ""}`,
    );
  }
  return names[0];
}

export function previewBootstrapStack(env = process.env) {
  return `${env.OCEL_NAMESPACE?.trim() || DEFAULT_NAMESPACE}-bootstrap-preview`;
}

export function resolveBootstrapBucket(logicalId, envHint, fail) {
  const found = aws([
    "cloudformation",
    "describe-stack-resources",
    "--stack-name",
    previewBootstrapStack(),
    "--query",
    `StackResources[?LogicalResourceId==\`${logicalId}\`].PhysicalResourceId | [0]`,
    "--output",
    "text",
  ]);
  if (!found || found === "None") {
    fail(`could not resolve the bootstrap's ${logicalId}; set ${envHint}`);
  }
  return found;
}
