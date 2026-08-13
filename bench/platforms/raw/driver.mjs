import { execFileSync, spawnSync } from "node:child_process";
import { createRequire } from "node:module";
import { existsSync, mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { pathToFileURL } from "node:url";

import {
  ARTIFACT_DIR,
  ASSUME_ROLE_POLICY,
  BASIC_EXECUTION_POLICY_ARN,
  BUNDLE_FILE,
  ESBUILD_VERSION,
  ESM_REQUIRE_BANNER,
  HANDLER_SOURCE,
  LAMBDA_HANDLER,
  ROLE_ARN_ENV,
  RUN_ID_ENV,
  STATE_FILE,
  TIMEOUT_SECONDS,
  TOOLCHAIN_DIR,
  logGroupName,
  pinProblems,
  rawFunctionName,
  rawRoleName,
  reclaimMessage,
  urlProblems,
} from "./lib.mjs";
import { zipArchive } from "./zip.mjs";

const ZIP_FILE = "function.zip";

const INSTALL_TIMEOUT_MS = 10 * 60 * 1000;

const AWS_TIMEOUT_MS = 120_000;

const WAIT_TIMEOUT_MS = 360_000;

const ROLE_PROPAGATION_ATTEMPTS = 20;

const ROLE_PROPAGATION_INTERVAL_MS = 3_000;

const NODE_TARGET = "node24";

const MINIFY = true;

export async function deploy({ app, workdir, region, pinned, env, log }) {
  const say = logger(log, "raw");
  const runId = process.env[RUN_ID_ENV] ?? process.env.GITHUB_RUN_ID;
  const functionName = rawFunctionName({ app: app?.name ?? app, workdir, runId });
  const roleName = rawRoleName(functionName);
  const providedRole = process.env[ROLE_ARN_ENV];
  const childEnv = { ...process.env, ...(env ?? {}) };

  writeState(workdir, { functionName, roleName, region, roleCreated: false });

  const artifactDir = join(workdir, ARTIFACT_DIR);
  mkdirSync(artifactDir, { recursive: true });
  const esbuild = await loadEsbuild(artifactDir, childEnv, say);

  const buildStart = performance.now();
  const built = await esbuild.build({
    entryPoints: [join(workdir, HANDLER_SOURCE)],
    absWorkingDir: workdir,
    bundle: true,
    write: false,
    platform: "node",
    format: "esm",
    target: NODE_TARGET,
    banner: { js: ESM_REQUIRE_BANNER },
    logLevel: "silent",
    minify: MINIFY,
    sourcemap: false,
  });
  const bundle = Buffer.from(built.outputFiles[0].contents);
  const archive = zipArchive([{ name: BUNDLE_FILE, data: bundle }]);
  const zipPath = join(artifactDir, ZIP_FILE);
  writeFileSync(zipPath, archive);
  const buildMs = performance.now() - buildStart;
  say(`bundled ${HANDLER_SOURCE} to ${bundle.length} bytes, zipped to ${archive.length}`);

  const provisionStart = performance.now();
  const roleArn = providedRole || createRole({ roleName, region, env: childEnv, say });
  writeState(workdir, { functionName, roleName, region, roleCreated: !providedRole });

  createFunction({ functionName, roleArn, region, zipPath, pinned, env: childEnv, say });
  aws(["lambda", "wait", "function-active-v2", "--function-name", functionName], {
    region,
    env: childEnv,
    timeout: WAIT_TIMEOUT_MS,
  });
  const urlConfig = JSON.parse(
    aws(
      [
        "lambda",
        "create-function-url-config",
        "--function-name",
        functionName,
        "--auth-type",
        "NONE",
        "--output",
        "json",
      ],
      { region, env: childEnv },
    ),
  );
  aws(
    [
      "lambda",
      "add-permission",
      "--function-name",
      functionName,
      "--statement-id",
      "bench-public-url",
      "--action",
      "lambda:InvokeFunctionUrl",
      "--principal",
      "*",
      "--function-url-auth-type",
      "NONE",
      "--output",
      "json",
    ],
    { region, env: childEnv },
  );
  aws(
    [
      "lambda",
      "add-permission",
      "--function-name",
      functionName,
      "--statement-id",
      "bench-public-invoke",
      "--action",
      "lambda:InvokeFunction",
      "--principal",
      "*",
      "--output",
      "json",
    ],
    { region, env: childEnv },
  );
  const provisionMs = performance.now() - provisionStart;

  const configuration = JSON.parse(
    aws(["lambda", "get-function-configuration", "--function-name", functionName, "--output", "json"], {
      region,
      env: childEnv,
    }),
  );
  const problems = [...pinProblems(configuration, pinned), ...urlProblems(urlConfig)];
  if (problems.length > 0) {
    throw new Error(
      `[bench/raw] ${functionName} did not come up on the pinned shape: ${problems.join("; ")}\n` +
        `[bench/raw] it is live and must be reclaimed before rerunning`,
    );
  }

  say(`${functionName} is live at ${urlConfig.FunctionUrl}`);
  return {
    url: urlConfig.FunctionUrl,
    functionName,
    buildMs,
    provisionMs,
    roleName,
    roleCreated: !providedRole,
  };
}

export async function teardown({ app, workdir, region, deployment, log }) {
  const say = logger(log, "raw");
  const state = readState(workdir);
  const runId = process.env[RUN_ID_ENV] ?? process.env.GITHUB_RUN_ID;
  const functionName =
    deployment?.functionName || state.functionName || rawFunctionName({ app: app?.name ?? app, workdir, runId });
  const roleName = deployment?.roleName || state.roleName || rawRoleName(functionName);
  const roleCreated = deployment?.roleCreated ?? state.roleCreated ?? !process.env[ROLE_ARN_ENV];
  const at = state.region || region;
  const problems = [];

  attempt(problems, `delete the function URL of ${functionName}`, () =>
    aws(["lambda", "delete-function-url-config", "--function-name", functionName], { region: at }),
  );
  attempt(problems, `delete the function ${functionName}`, () =>
    aws(["lambda", "delete-function", "--function-name", functionName], { region: at }),
  );
  attempt(problems, `delete the log group ${logGroupName(functionName)}`, () =>
    aws(["logs", "delete-log-group", "--log-group-name", logGroupName(functionName)], { region: at }),
  );
  if (roleCreated) {
    attempt(problems, `detach ${BASIC_EXECUTION_POLICY_ARN} from ${roleName}`, () =>
      aws(["iam", "detach-role-policy", "--role-name", roleName, "--policy-arn", BASIC_EXECUTION_POLICY_ARN], {
        region: at,
      }),
    );
    attempt(problems, `delete the role ${roleName}`, () =>
      aws(["iam", "delete-role", "--role-name", roleName], { region: at }),
    );
  }

  if (problems.length > 0) {
    throw new Error(reclaimMessage({ functionName, roleName, roleCreated, region: at, problems }));
  }
  say(`${functionName} removed`);
}

function createRole({ roleName, region, env, say }) {
  const created = JSON.parse(
    aws(
      [
        "iam",
        "create-role",
        "--role-name",
        roleName,
        "--assume-role-policy-document",
        JSON.stringify(ASSUME_ROLE_POLICY),
        "--output",
        "json",
      ],
      { region, env },
    ),
  );
  aws(
    ["iam", "attach-role-policy", "--role-name", roleName, "--policy-arn", BASIC_EXECUTION_POLICY_ARN],
    { region, env },
  );
  say(`created execution role ${roleName}`);
  return created.Role.Arn;
}

function createFunction({ functionName, roleArn, region, zipPath, pinned, env, say }) {
  const args = [
    "lambda",
    "create-function",
    "--function-name",
    functionName,
    "--runtime",
    pinned.runtime,
    "--architectures",
    pinned.architecture,
    "--memory-size",
    String(pinned.memoryMB),
    "--timeout",
    String(TIMEOUT_SECONDS),
    "--package-type",
    "Zip",
    "--role",
    roleArn,
    "--handler",
    LAMBDA_HANDLER,
    "--zip-file",
    `fileb://${zipPath}`,
    "--output",
    "json",
  ];
  for (let attemptNumber = 1; ; attemptNumber += 1) {
    try {
      return JSON.parse(aws(args, { region, env, timeout: WAIT_TIMEOUT_MS }));
    } catch (err) {
      const stderr = String(err.stderr ?? err.message ?? "");
      const propagating = /cannot be assumed|InvalidParameterValueException/.test(stderr);
      if (!propagating || attemptNumber >= ROLE_PROPAGATION_ATTEMPTS) throw err;
      say(`${roleArn} has not propagated yet (attempt ${attemptNumber}); retrying`);
      sleepSync(ROLE_PROPAGATION_INTERVAL_MS);
    }
  }
}

async function loadEsbuild(artifactDir, env, say) {
  const dir = join(artifactDir, TOOLCHAIN_DIR);
  const manifest = join(dir, "package.json");
  if (!existsSync(join(dir, "node_modules", "esbuild", "package.json"))) {
    mkdirSync(dir, { recursive: true });
    writeFileSync(
      manifest,
      `${JSON.stringify({ name: "bench-raw-toolchain", private: true, version: "0.0.0" }, null, 2)}\n`,
    );
    say(`installing esbuild@${ESBUILD_VERSION} into ${dir}`);
    const result = spawnSync(
      "npm",
      ["install", "--no-audit", "--no-fund", "--loglevel", "error", `esbuild@${ESBUILD_VERSION}`],
      { cwd: dir, env, encoding: "utf8", timeout: INSTALL_TIMEOUT_MS, stdio: ["ignore", "pipe", "pipe"] },
    );
    if (result.error || result.status !== 0) {
      throw new Error(
        `[bench/raw] could not install esbuild@${ESBUILD_VERSION} into ${dir}; ` +
          `the raw baseline cannot bundle ${HANDLER_SOURCE} without it: ` +
          `${result.error?.message ?? String(result.stderr ?? "").trim()}`,
      );
    }
  }
  return import(pathToFileURL(createRequire(manifest).resolve("esbuild")).href);
}

function aws(args, { region, env, timeout } = {}) {
  return execFileSync("aws", ["--region", region, ...args], {
    encoding: "utf8",
    timeout: timeout ?? AWS_TIMEOUT_MS,
    stdio: ["ignore", "pipe", "pipe"],
    maxBuffer: 64 * 1024 * 1024,
    env: { ...(env ?? process.env), AWS_RETRY_MODE: "standard", AWS_MAX_ATTEMPTS: "4" },
  }).trim();
}

function attempt(problems, what, run) {
  try {
    run();
  } catch (err) {
    const stderr = String(err.stderr ?? err.message ?? "");
    if (/ResourceNotFoundException|NoSuchEntity|ResourceNotFound/.test(stderr)) return;
    problems.push(`could not ${what}: ${stderr.trim().split("\n").pop()}`);
  }
}

function statePath(workdir) {
  return join(workdir, STATE_FILE);
}

function writeState(workdir, state) {
  writeFileSync(statePath(workdir), `${JSON.stringify(state, null, 2)}\n`);
}

function readState(workdir) {
  const path = statePath(workdir);
  if (!existsSync(path)) return {};
  try {
    return JSON.parse(readFileSync(path, "utf8")) ?? {};
  } catch {
    return {};
  }
}

function sleepSync(ms) {
  Atomics.wait(new Int32Array(new SharedArrayBuffer(4)), 0, 0, ms);
}

function logger(log, tag) {
  if (typeof log === "function") return (message) => log(message);
  return (message) => console.error(`[bench/${tag}] ${message}`);
}
