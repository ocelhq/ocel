import { execFileSync, spawnSync } from "node:child_process";
import { existsSync, mkdirSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { join } from "node:path";

import {
  BOOTSTRAP_PARAMETER,
  CONFIG_FILE,
  OUTPUTS_FILE,
  RUN_ID_ENV,
  SST_VERSION,
  STATE_FILE,
  TOOLCHAIN_DIR,
  outputsProblems,
  passphraseParameter,
  pinProblems,
  reclaimMessage,
  renderSstConfig,
  sstAppName,
  sstStage,
} from "./config.mjs";

const AWS_TIMEOUT_MS = 120_000;

const INSTALL_TIMEOUT_MS = 10 * 60 * 1000;

const DEPLOY_TIMEOUT_MS = 25 * 60 * 1000;

const REMOVE_TIMEOUT_MS = 25 * 60 * 1000;

const LOG_TAIL_LINES = 40;

const PINNED_FALLBACK = Object.freeze({ runtime: "nodejs24.x", memoryMB: 1024, architecture: "x86_64" });

export async function deploy({ app, workdir, region, pinned, env, log }) {
  const say = logger(log);
  const appName = sstAppName(app?.name ?? app);
  const stage = sstStage({ workdir, runId: process.env[RUN_ID_ENV] ?? process.env.GITHUB_RUN_ID });
  const childEnv = { ...process.env, ...(env ?? {}), AWS_REGION: region, NO_COLOR: "1", CI: "1" };

  writeState(workdir, { appName, stage, region, pinned });
  writeFileSync(join(workdir, CONFIG_FILE), renderSstConfig({ appName, region, pinned }));
  const sst = ensureToolchain(workdir, childEnv, say);

  run(sst, ["install"], { cwd: workdir, env: childEnv, timeout: INSTALL_TIMEOUT_MS });
  rmSync(join(workdir, OUTPUTS_FILE), { force: true });

  say(`deploying ${appName} stage ${stage} to ${region}`);
  const provisionStart = performance.now();
  run(sst, ["deploy", "--stage", stage], { cwd: workdir, env: childEnv, timeout: DEPLOY_TIMEOUT_MS });
  const provisionMs = performance.now() - provisionStart;

  const outputs = readOutputs(workdir);
  const problems = outputsProblems(outputs);
  if (problems.length > 0) {
    throw new Error(
      `[bench/sst] ${appName} stage ${stage} deployed but its outputs are unusable: ${problems.join("; ")}\n` +
        `[bench/sst] the stage is live; remove it with \`${sst} remove --stage ${stage}\` from ${workdir}`,
    );
  }

  const configuration = JSON.parse(
    aws(["lambda", "get-function-configuration", "--function-name", outputs.functionName, "--output", "json"], {
      region,
      env: childEnv,
    }),
  );
  const urlConfig = JSON.parse(
    aws(["lambda", "get-function-url-config", "--function-name", outputs.functionName, "--output", "json"], {
      region,
      env: childEnv,
    }),
  );
  const pinFailures = [
    ...pinProblems(configuration, pinned),
    ...(urlConfig?.AuthType === "NONE"
      ? []
      : [`the function URL is AuthType ${JSON.stringify(urlConfig?.AuthType)}, not NONE, so it will 403`]),
  ];
  if (pinFailures.length > 0) {
    throw new Error(
      `[bench/sst] ${outputs.functionName} did not come up on the pinned shape: ${pinFailures.join("; ")}\n` +
        `[bench/sst] the stage is live; remove it with \`${sst} remove --stage ${stage}\` from ${workdir}`,
    );
  }

  say(`${outputs.functionName} is live at ${outputs.url}`);
  const logGroupName = resolveLogGroup({ appName, stage, region, env: childEnv, say });
  return {
    url: outputs.url,
    functionName: outputs.functionName,
    logGroupName,
    buildMs: 0,
    provisionMs,
    appName,
    stage,
  };
}

export async function teardown({ app, workdir, region, deployment, log }) {
  const say = logger(log);
  const state = readState(workdir);
  const appName =
    deployment?.appName || state.appName || sstAppName(app?.name ?? app);
  const stage =
    deployment?.stage ||
    state.stage ||
    sstStage({ workdir, runId: process.env[RUN_ID_ENV] ?? process.env.GITHUB_RUN_ID });
  const at = state.region || region;
  const childEnv = { ...process.env, AWS_REGION: at, NO_COLOR: "1", CI: "1" };
  const problems = [];

  if (!existsSync(join(workdir, CONFIG_FILE))) {
    say(`no ${CONFIG_FILE}; re-rendering it so \`sst remove\` can find the stage`);
    writeFileSync(
      join(workdir, CONFIG_FILE),
      renderSstConfig({ appName, region: at, pinned: state.pinned ?? PINNED_FALLBACK }),
    );
  }

  let sst = null;
  try {
    sst = ensureToolchain(workdir, childEnv, say);
  } catch (err) {
    problems.push(`could not install the sst CLI: ${String(err.message).split("\n")[0]}`);
  }

  if (sst) {
    attempt(problems, `remove stage ${stage}`, () =>
      run(sst, ["remove", "--stage", stage], { cwd: workdir, env: childEnv, timeout: REMOVE_TIMEOUT_MS }),
    );
  }
  attempt(problems, `delete ${passphraseParameter(appName, stage)}`, () =>
    aws(["ssm", "delete-parameter", "--name", passphraseParameter(appName, stage)], { region: at, env: childEnv }),
  );

  if (problems.length > 0) {
    throw new Error(reclaimMessage({ appName, stage, region: at, workdir, problems }));
  }
  say(`stage ${stage} of ${appName} removed; SST's bootstrap in ${at} is untouched by design`);
}

export async function removeBootstrap({ region, confirm, log } = {}) {
  const say = logger(log);
  if (!confirm) {
    throw new Error(
      `[bench/sst] removeBootstrap deletes SST's account-wide state in ${region} and would break any other ` +
        `SST app in this account; call it with { confirm: true } once the whole matrix has finished`,
    );
  }
  const problems = [];
  const buckets = listBuckets(region).filter((name) => /^sst-(asset|state)-/.test(name));
  for (const bucket of buckets) {
    attempt(problems, `empty and delete ${bucket}`, () => {
      aws(["s3", "rm", `s3://${bucket}`, "--recursive"], { region, timeout: DEPLOY_TIMEOUT_MS });
      aws(["s3api", "delete-bucket", "--bucket", bucket], { region });
    });
  }
  attempt(problems, `delete the sst-asset ECR repository`, () =>
    aws(["ecr", "delete-repository", "--repository-name", "sst-asset", "--force"], { region }),
  );
  attempt(problems, `delete ${BOOTSTRAP_PARAMETER}`, () =>
    aws(["ssm", "delete-parameter", "--name", BOOTSTRAP_PARAMETER], { region }),
  );
  if (problems.length > 0) {
    throw new Error(
      `[bench/sst] BOOTSTRAP TEARDOWN FAILED in ${region}: ${problems.join("; ")}\n` +
        `[bench/sst] the leftovers are still billable; list them with ` +
        `\`aws s3api list-buckets --query "Buckets[?starts_with(Name,'sst-')].Name"\` and ` +
        `\`aws ssm get-parameter --region ${region} --name ${BOOTSTRAP_PARAMETER}\``,
    );
  }
  say(`removed ${buckets.length} sst bucket(s) and ${BOOTSTRAP_PARAMETER} in ${region}`);
}

function ensureToolchain(workdir, env, say) {
  const dir = join(workdir, TOOLCHAIN_DIR);
  const bin = join(dir, "node_modules", ".bin", "sst");
  if (existsSync(bin)) return bin;
  mkdirSync(dir, { recursive: true });
  writeFileSync(
    join(dir, "package.json"),
    `${JSON.stringify({ name: "bench-sst-toolchain", private: true, version: "0.0.0" }, null, 2)}\n`,
  );
  say(`installing sst@${SST_VERSION} into ${dir}`);
  run("npm", ["install", "--no-audit", "--no-fund", "--loglevel", "error", `sst@${SST_VERSION}`], {
    cwd: dir,
    env,
    timeout: INSTALL_TIMEOUT_MS,
  });
  if (!existsSync(bin)) {
    throw new Error(`sst@${SST_VERSION} installed into ${dir} but left no ${bin}`);
  }
  return bin;
}

function readOutputs(workdir) {
  const path = join(workdir, OUTPUTS_FILE);
  if (!existsSync(path)) {
    throw new Error(
      `[bench/sst] sst deploy left no ${OUTPUTS_FILE} in ${workdir}; ` +
        `without it the deployed Lambda's name cannot be read`,
    );
  }
  return JSON.parse(readFileSync(path, "utf8"));
}

function run(command, args, { cwd, env, timeout }) {
  const result = spawnSync(command, args, {
    cwd,
    env,
    encoding: "utf8",
    timeout,
    stdio: ["ignore", "pipe", "pipe"],
    maxBuffer: 64 * 1024 * 1024,
  });
  if (result.error || result.signal || result.status !== 0) {
    const why = result.error?.message ?? (result.signal ? `killed with ${result.signal}` : `exited with ${result.status}`);
    throw new Error(
      `${command} ${args.join(" ")} ${why}\n${tail(`${result.stdout ?? ""}${result.stderr ?? ""}`, LOG_TAIL_LINES)}`,
    );
  }
  return result.stdout ?? "";
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

function resolveLogGroup({ appName, stage, region, env, say }) {
  const prefix = `/aws/lambda/${appName}-${stage}-`;
  try {
    const response = JSON.parse(
      aws(["logs", "describe-log-groups", "--log-group-name-prefix", prefix, "--output", "json"], {
        region,
        env,
      }),
    );
    const names = (response.logGroups ?? []).map((group) => group.logGroupName);
    if (names.length === 1) return names[0];
    say(
      `${names.length} log groups start with ${prefix}; falling back to the function's own name, which sst does not ` +
        `follow for log groups, so no REPORT line may be read`,
    );
  } catch (err) {
    say(`could not resolve the log group under ${prefix}: ${String(err.stderr ?? err.message ?? "").trim()}`);
  }
  return null;
}

function listBuckets(region) {
  const response = JSON.parse(aws(["s3api", "list-buckets", "--output", "json"], { region }));
  return (response.Buckets ?? []).map((bucket) => bucket.Name);
}

function attempt(problems, what, action) {
  try {
    action();
  } catch (err) {
    const text = String(err.stderr ?? err.message ?? "");
    if (/ParameterNotFound|NoSuchBucket|RepositoryNotFoundException|ResourceNotFoundException/.test(text)) return;
    problems.push(`could not ${what}: ${tail(text.trim(), 3)}`);
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

function tail(text, lines) {
  const all = String(text ?? "").split("\n");
  return all.slice(Math.max(0, all.length - lines)).join("\n");
}

function logger(log) {
  if (typeof log === "function") return (message) => log(message);
  return (message) => console.error(`[bench/sst] ${message}`);
}
