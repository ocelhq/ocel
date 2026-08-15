import { execFileSync } from "node:child_process";

export const AWS_TIMEOUT_MS = 30_000;

export const AWS_CLI_RETRY_ENV = Object.freeze({ AWS_RETRY_MODE: "standard", AWS_MAX_ATTEMPTS: "4" });

export function aws(args) {
  return execFileSync("aws", args, {
    encoding: "utf8",
    timeout: AWS_TIMEOUT_MS,
    stdio: ["ignore", "pipe", "pipe"],
    maxBuffer: 16 * 1024 * 1024,
    env: { ...process.env, ...AWS_CLI_RETRY_ENV },
  }).trim();
}

export function awsUnreachable() {
  try {
    aws(["sts", "get-caller-identity", "--query", "Account", "--output", "text"]);
    return null;
  } catch (err) {
    return String(err.stderr || err.message).trim().split("\n").pop();
  }
}

export function varsTable() {
  const raw = aws([
    "cloudformation",
    "describe-stacks",
    "--stack-name",
    "ocel-bootstrap",
    "--query",
    "Stacks[0].Outputs[?OutputKey=='VarsTableName'].OutputValue",
    "--output",
    "text",
  ]);
  return raw || null;
}

export function partitionRows(table, pk) {
  const raw = aws([
    "dynamodb",
    "query",
    "--table-name",
    table,
    "--consistent-read",
    "--key-condition-expression",
    "pk = :pk",
    "--expression-attribute-values",
    JSON.stringify({ ":pk": { S: pk } }),
    "--output",
    "json",
  ]);
  const rows = {};
  for (const item of JSON.parse(raw).Items ?? []) {
    rows[item.sk.S] = item;
  }
  return rows;
}

export function taggedFunctionArns(tags) {
  const filters = Object.entries(tags).map(([key, value]) => `Key=${key},Values=${value}`);
  const raw = aws([
    "resourcegroupstaggingapi",
    "get-resources",
    "--resource-type-filters",
    "lambda:function",
    "--tag-filters",
    ...filters,
    "--output",
    "json",
  ]);
  return (JSON.parse(raw).ResourceTagMappingList ?? []).map((entry) => entry.ResourceARN);
}

export function functionConfiguration(arn) {
  return JSON.parse(
    aws(["lambda", "get-function-configuration", "--function-name", arn, "--output", "json"]),
  );
}

export function roleInlinePolicies(roleName) {
  const names = JSON.parse(
    aws(["iam", "list-role-policies", "--role-name", roleName, "--output", "json"]),
  ).PolicyNames ?? [];
  return names.map((name) => {
    const raw = aws([
      "iam",
      "get-role-policy",
      "--role-name",
      roleName,
      "--policy-name",
      name,
      "--output",
      "json",
    ]);
    return JSON.parse(raw).PolicyDocument;
  });
}

export function roleNameOf(roleArn) {
  return String(roleArn ?? "").split("/").pop();
}

export function item(table, pk, sk) {
  const raw = aws([
    "dynamodb",
    "get-item",
    "--table-name",
    table,
    "--consistent-read",
    "--key",
    JSON.stringify({ pk: { S: pk }, sk: { S: sk } }),
    "--output",
    "json",
  ]);
  return JSON.parse(raw).Item ?? null;
}
