import { createHash } from "node:crypto";

export const HANDLER_SOURCE = "src/handler.ts";

export const HANDLER_EXPORT = "handler";

export const BUNDLE_FILE = "index.mjs";

export const LAMBDA_HANDLER = `index.${HANDLER_EXPORT}`;

export const ARTIFACT_DIR = ".bench-raw";

export const STATE_FILE = ".bench-raw.json";

export const TIMEOUT_SECONDS = 30;

export const BASIC_EXECUTION_POLICY_ARN = "arn:aws:iam::aws:policy/service-role/AWSLambdaBasicExecutionRole";

export const ROLE_ARN_ENV = "BENCH_LAMBDA_ROLE_ARN";

export const RUN_ID_ENV = "BENCH_RUN_ID";

export const NAME_PREFIX = "bench-raw";

export const LAMBDA_NAME_MAX = 64;

export const TOKEN_LEN = 8;

export const ESM_REQUIRE_BANNER = [
  `import { createRequire as __benchCreateRequire } from "node:module";`,
  `const require = __benchCreateRequire(import.meta.url);`,
].join("\n");

export const ASSUME_ROLE_POLICY = Object.freeze({
  Version: "2012-10-17",
  Statement: [
    {
      Effect: "Allow",
      Principal: { Service: "lambda.amazonaws.com" },
      Action: "sts:AssumeRole",
    },
  ],
});

function sanitize(value) {
  return String(value ?? "")
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-+|-+$/g, "");
}

function hash(value) {
  return createHash("sha256").update(String(value ?? "")).digest("hex").slice(0, TOKEN_LEN);
}

export function cellToken({ workdir, runId }) {
  const run = sanitize(runId).slice(0, TOKEN_LEN).replace(/-+$/, "");
  const place = hash(String(workdir ?? "").replace(/\/+$/, ""));
  return run ? `${run}-${place}` : place;
}

export function rawFunctionName({ app, workdir, runId }) {
  const name = `${NAME_PREFIX}-${sanitize(app) || "app"}-${cellToken({ workdir, runId })}`;
  if (name.length > LAMBDA_NAME_MAX) {
    throw new Error(`${name} is ${name.length} characters; a Lambda name caps at ${LAMBDA_NAME_MAX}`);
  }
  return name;
}

export function rawRoleName(functionName) {
  return `${functionName}-role`;
}

export function logGroupName(functionName) {
  return `/aws/lambda/${functionName}`;
}

export function pinProblems(configuration, pinned) {
  const problems = [];
  const runtime = configuration?.Runtime;
  const memory = configuration?.MemorySize;
  const architecture = (configuration?.Architectures ?? [])[0];
  if (runtime !== pinned?.runtime) {
    problems.push(`runtime is ${JSON.stringify(runtime)}, not the pinned ${JSON.stringify(pinned?.runtime)}`);
  }
  if (Number(memory) !== Number(pinned?.memoryMB)) {
    problems.push(`memory is ${JSON.stringify(memory)} MB, not the pinned ${JSON.stringify(pinned?.memoryMB)} MB`);
  }
  if (architecture !== pinned?.architecture) {
    problems.push(
      `architecture is ${JSON.stringify(architecture)}, not the pinned ${JSON.stringify(pinned?.architecture)}`,
    );
  }
  return problems;
}

export function urlProblems(config) {
  const problems = [];
  if (config?.AuthType !== "NONE") {
    problems.push(`the function URL is AuthType ${JSON.stringify(config?.AuthType)}, not NONE, so it will 403`);
  }
  if (typeof config?.FunctionUrl !== "string" || !/^https?:\/\//.test(config?.FunctionUrl ?? "")) {
    problems.push(`the function URL is ${JSON.stringify(config?.FunctionUrl)}, not an http(s) URL`);
  }
  return problems;
}

export const ESBUILD_VERSION = "0.28.2";

export const TOOLCHAIN_DIR = "toolchain";

export function reclaimMessage({ functionName, roleName, roleCreated, region, problems }) {
  const commands = [
    `aws lambda delete-function --region ${region} --function-name ${functionName}`,
    `aws logs delete-log-group --region ${region} --log-group-name ${logGroupName(functionName)}`,
    ...(roleCreated
      ? [
          `aws iam detach-role-policy --role-name ${roleName} --policy-arn ${BASIC_EXECUTION_POLICY_ARN}`,
          `aws iam delete-role --role-name ${roleName}`,
        ]
      : []),
  ];
  return [
    `[bench/raw] TEARDOWN FAILED for ${functionName} in ${region}: ${problems.join("; ")}`,
    `[bench/raw] it is still live and still billable; reclaim it by running:`,
    ...commands.map((command) => `  ${command}`),
  ].join("\n");
}
