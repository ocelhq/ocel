import { spawn } from "node:child_process";
import { createWriteStream, existsSync, lstatSync, mkdirSync, readFileSync, rmSync, symlinkSync, writeFileSync } from "node:fs";
import { join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

import { aws, awsUnreachable } from "../../../scripts/e2e-node/aws.mjs";
import { SKIP_BUILD_ENV, buildToolchain, linkOcel } from "../../../scripts/e2e-node/toolchain.mjs";
import {
  DEPLOY_RESULT_FILE,
  DESTROY_BYPASS_ENV,
  LOG_FILE,
  PROD_ENV_SEGMENT,
  STATE_FILE,
  appURLProblem,
  baseDomainFor,
  baseDomainProblem,
  cellSlugFor,
  functionNameVerdict,
  lambdaFunctionNames,
  nativeProviderPackage,
  pinnedProblems,
  productionEnvProblem,
  productionHost,
  renderOcelConfig,
  resolveAppURL,
  slugProblem,
  tail,
} from "./lib.mjs";

function adapterRoot() {
  return process.env.ADAPTER_DIR || resolve(fileURLToPath(import.meta.url), "..", "..", "..", "..");
}

function linkNativeProvider(workdir, adapterDir) {
  const name = nativeProviderPackage();
  const target = join(adapterDir, "packages", "native-lib", name);
  if (!existsSync(join(target, "package.json"))) {
    throw new Error(
      `${target} is not a package; the provider binary is built by \`node scripts/build-native.mjs --host --target provider\``,
    );
  }
  const link = join(workdir, "node_modules", "@ocel", name);
  mkdirSync(join(link, ".."), { recursive: true });
  if (existsSync(link) || isSymlink(link)) {
    rmSync(link, { recursive: true, force: true });
  }
  symlinkSync(target, link, "dir");
}

function isSymlink(path) {
  try {
    return lstatSync(path).isSymbolicLink();
  } catch {
    return false;
  }
}

const BUILD_TIMEOUT_MS =Number(process.env.BENCH_OCEL_BUILD_TIMEOUT_MS) || 15 * 60 * 1000;

const PROVISION_TIMEOUT_MS = Number(process.env.BENCH_OCEL_PROVISION_TIMEOUT_MS) || 25 * 60 * 1000;

const TEARDOWN_TIMEOUT_MS = Number(process.env.BENCH_OCEL_TEARDOWN_TIMEOUT_MS) || 30 * 60 * 1000;

const FAILURE_LOG_LINES = 120;

let toolchain = null;

export async function deploy({ app, platform, workdir, region, pinned, env = {}, log = () => {} }) {
  const id = platformId(platform);
  const adapterDir = requireCredentials(log);
  const { slug, host } = cellIdentity({ app: app.name, platform: id });

  await buildToolchainOnce(adapterDir, log);

  writeFileSync(
    join(workdir, STATE_FILE),
    `${JSON.stringify({ slug, host, app: app.name, framework: app.framework, platform: id, region }, null, 2)}\n`,
  );
  writeFileSync(
    join(workdir, "ocel.config.ts"),
    renderOcelConfig({ slug, app: app.name, framework: app.framework, host }),
  );
  linkOcel(workdir, adapterDir);
  linkNativeProvider(workdir, adapterDir);
  log(`project ${slug} serving ${host} from ${workdir}`);

  const childEnv = { ...process.env, ...regionEnv(region), ...env };

  const buildMs = await runOcel({ adapterDir, workdir, env: childEnv, log }, ["build"], BUILD_TIMEOUT_MS);
  const provisionMs = await runOcel(
    { adapterDir, workdir, env: childEnv, log },
    ["deploy", "--prebuilt", "--yes"],
    PROVISION_TIMEOUT_MS,
  );

  const result = readDeployResult(workdir);
  const envProblem = productionEnvProblem(result.environment);
  if (envProblem) throw new Error(`${app.name} on ${id}: ${envProblem}`);

  const { url, problem } = resolveAppURL(result, { host });
  if (problem) throw new Error(`${app.name} on ${id}: ${problem}`);
  const urlProblem = appURLProblem(url, { host });
  if (urlProblem) throw new Error(`${app.name} on ${id}: ${urlProblem}`);

  const functionName = resolveFunctionName({ slug, app: app.name, region });
  assertPinned({ functionName, region, pinned, app: app.name, platform: id, log });

  log(`${app.name} on ${id}: ${url} served by ${functionName}`);
  return { url, functionName, buildMs, provisionMs, slug, host };
}

export async function teardown({ app, platform, workdir, region, deployment, log = () => {} }) {
  const id = platformId(platform);
  const adapterDir = adapterRoot();
  const { slug, host } = recordedIdentity({ app, platform: id, workdir, deployment, log });

  const configPath = join(workdir, "ocel.config.ts");
  if (!existsSync(configPath)) {
    writeFileSync(configPath, renderOcelConfig({ slug, app: app.name, framework: app.framework, host }));
    linkOcel(workdir, adapterDir);
    linkNativeProvider(workdir, adapterDir);
  }

  log(`destroying project ${slug}`);
  try {
    await runOcel(
      {
        adapterDir,
        workdir,
        env: { ...process.env, ...regionEnv(region), [DESTROY_BYPASS_ENV]: "1" },
        log,
      },
      ["destroy"],
      TEARDOWN_TIMEOUT_MS,
    );
  } catch (err) {
    throw new Error(
      `TEARDOWN FAILED for project ${slug} (${app.name} on ${id}): ${err.message}\n` +
        `Its Lambda, its app-deploy and infra stacks, its edge worker and the ${host} route and DNS record are ` +
        `still live and still billing, and the slug and the hostname both stay taken. Reclaim them with ` +
        `\`${DESTROY_BYPASS_ENV}=1 ocel destroy\` run from ${workdir}, or from any directory whose ocel.config.ts ` +
        `declares slug: "${slug}"`,
    );
  }
  log(`project ${slug} destroyed`);
}

async function buildToolchainOnce(adapterDir, log) {
  if (!toolchain) {
    if (process.env.BENCH_SKIP_TOOLCHAIN_BUILD === "1") {
      process.env[SKIP_BUILD_ENV] = "1";
    }
    toolchain = Promise.resolve().then(() => {
      log("building the toolchain this benchmark deploys with (once for the whole run)");
      buildToolchain(adapterDir);
    });
  }
  await toolchain;
}

function requireCredentials(log) {
  const missing = ["OCEL_ACCESS_TOKEN", "CLOUDFLARE_API_TOKEN", "CLOUDFLARE_ACCOUNT_ID"].filter(
    (name) => !process.env[name],
  );
  if (missing.length > 0) {
    throw new Error(
      `${missing.join(", ")} ${missing.length === 1 ? "is" : "are"} not set; scripts/e2e-node/README.md lists what ` +
        `each one is. Deploying without them fails partway and strands whatever was already created`,
    );
  }
  const unreachable = awsUnreachable();
  if (unreachable) {
    throw new Error(
      `AWS is not reachable from here (${unreachable}), and this driver needs the aws CLI both to deploy and to ` +
        `name the deployed function; without the name no cold start can be forced`,
    );
  }
  const adapterDir = adapterRoot();
  log(`deploying with the toolchain at ${adapterDir}`);
  return adapterDir;
}

function cellIdentity({ app, platform }) {
  const slug = cellSlugFor({ app, platform });
  const baseDomain = baseDomainFor();
  const zoneProblem = baseDomainProblem(baseDomain);
  if (zoneProblem) throw new Error(zoneProblem);
  const problem = slugProblem(slug);
  if (problem) throw new Error(`this cell cannot be given a project of its own: ${problem}`);
  return { slug, host: productionHost(slug, baseDomain) };
}

function platformId(platform) {
  const id = typeof platform === "string" ? platform : platform?.id;
  if (!id) {
    throw new Error(`this driver was handed ${JSON.stringify(platform)} as its platform; it needs the matrix id`);
  }
  return id;
}

function regionEnv(region) {
  if (!region) return {};
  return { AWS_REGION: region, AWS_DEFAULT_REGION: region };
}

function runOcel({ adapterDir, workdir, env, log }, args, timeoutMs) {
  const label = `ocel ${args[0]}`;
  const command = [join(adapterDir, "packages", "ocel", "bin", "run.js"), ...args];
  const logPath = join(workdir, LOG_FILE);
  const sink = createWriteStream(logPath, { flags: "a" });

  return new Promise((resolve, reject) => {
    const startedAt = performance.now();
    const child = spawn(process.execPath, command, {
      cwd: workdir,
      env,
      stdio: ["ignore", "pipe", "pipe"],
    });
    child.stdout.pipe(sink, { end: false });
    child.stderr.pipe(sink, { end: false });

    const timer = setTimeout(() => {
      child.kill("SIGTERM");
    }, timeoutMs);

    child.once("error", (err) => {
      clearTimeout(timer);
      sink.end();
      reject(new Error(`${label}: ${err.message}`));
    });
    child.once("close", (code, signal) => {
      clearTimeout(timer);
      const elapsed = performance.now() - startedAt;
      sink.end();
      if (signal) {
        reject(
          new Error(
            `${label} timed out after ${Math.round(timeoutMs / 1000)}s and was killed with ${signal}\n${logTail(workdir)}`,
          ),
        );
        return;
      }
      if (code !== 0) {
        reject(new Error(`${label} exited with ${code}\n${logTail(workdir)}`));
        return;
      }
      log(`${label} took ${Math.round(elapsed)}ms`);
      resolve(elapsed);
    });
  });
}

function logTail(workdir) {
  const path = join(workdir, LOG_FILE);
  if (!existsSync(path)) return `(no ${LOG_FILE} in ${workdir})`;
  return `last ${FAILURE_LOG_LINES} lines of ${path}:\n${tail(readFileSync(path, "utf8"), FAILURE_LOG_LINES)}`;
}

function readDeployResult(workdir) {
  const path = join(workdir, DEPLOY_RESULT_FILE);
  if (!existsSync(path)) {
    throw new Error(`${path} was not written; the deploy reported success but produced no result`);
  }
  return JSON.parse(readFileSync(path, "utf8"));
}

function resolveFunctionName({ slug, app, region }) {
  const names = lambdaFunctionNames(
    JSON.parse(
      aws([
        ...(region ? ["--region", region] : []),
        "resourcegroupstaggingapi",
        "get-resources",
        "--tag-filters",
        `Key=ocel:project,Values=${slug}`,
        `Key=ocel:app,Values=${app}`,
        `Key=ocel:env,Values=${PROD_ENV_SEGMENT}`,
        "--resource-type-filters",
        "lambda:function",
        "--output",
        "json",
      ]),
    ),
  );
  const { name, problem } = functionNameVerdict(names, { slug, app });
  if (problem) throw new Error(problem);
  return name;
}

function assertPinned({ functionName, region, pinned, app, platform, log }) {
  const config = JSON.parse(
    aws([
      ...(region ? ["--region", region] : []),
      "lambda",
      "get-function-configuration",
      "--function-name",
      functionName,
      "--output",
      "json",
    ]),
  );
  const problems = pinnedProblems(config, pinned);
  if (problems.length > 0) {
    throw new Error(
      `${app} on ${platform}: ${functionName} does not run on the machine this benchmark pins — ` +
        `${problems.join("; ")}. Every other cell would be compared against different hardware`,
    );
  }
  log(`${functionName} runs ${config.Runtime} on ${config.Architectures?.join(",")} with ${config.MemorySize}MB`);
}

function recordedIdentity({ app, platform, workdir, deployment, log }) {
  if (deployment?.slug && deployment?.host) {
    return { slug: deployment.slug, host: deployment.host };
  }
  try {
    const state = JSON.parse(readFileSync(join(workdir, STATE_FILE), "utf8"));
    if (state?.slug && state?.host) return { slug: state.slug, host: state.host };
  } catch {
    log(`no readable ${STATE_FILE} in ${workdir}; re-deriving this cell's project slug and hostname`);
  }
  return cellIdentity({ app: app.name, platform });
}
