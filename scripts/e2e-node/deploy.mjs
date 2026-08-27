#!/usr/bin/env node

import { spawnSync } from "node:child_process";
import { closeSync, existsSync, openSync, readFileSync, writeFileSync } from "node:fs";
import { join } from "node:path";

import {
  BUILD_LOG_FILE,
  DEPLOY_RESULT_FILE,
  SKIP_DRIFT_CHECK_ENV,
  SMOKE_APPS,
  STATE_FILE,
  planProblems,
  previewLabelProblem,
  previewRefForApp,
  projectSlugForRun,
  renderOcelConfig,
  resolveAppURLs,
  tail,
} from "./lib.mjs";
import { buildToolchain, linkOcel } from "./toolchain.mjs";

const DEFAULT_TIMEOUT_MS = 25 * 60 * 1000;

const deadline = Date.now() + (Number(process.env.OCEL_E2E_DEPLOY_TIMEOUT_MS) || DEFAULT_TIMEOUT_MS);

const FAILURE_LOG_LINES = 200;

const appDir = process.cwd();

const CHILD_ENV = {
  ...process.env,
  OCEL_EDGE_OBSERVABILITY: "off",
  ...SKIP_DRIFT_CHECK_ENV,
};

try {
  for (const line of deploy()) {
    process.stdout.write(`${line}\n`);
  }
} catch (err) {
  console.error(`[ocel-e2e-node] deploy failed: ${err.message}`);
  echoLogTail();
  process.exit(1);
}

function deploy() {
  const adapterDir = required("ADAPTER_DIR");

  const slug = projectSlugForRun();
  const ref = previewRefForApp(appDir);
  const problems = previewLabelProblem(slug, ref);
  if (problems.length > 0) {
    throw new Error(`the preview hostnames this run would ask for are too long:\n  ${problems.join("\n  ")}`);
  }

  buildToolchain(adapterDir);

  writeFileSync(
    join(appDir, STATE_FILE),
    `${JSON.stringify({ slug, ref, apps: SMOKE_APPS, startedAt: Date.now() }, null, 2)}\n`,
  );
  console.error(`[ocel-e2e-node] preview ${ref} of project ${slug} in ${appDir}`);

  writeFileSync(join(appDir, "ocel.config.ts"), renderOcelConfig({ slug }));
  linkOcel(appDir, adapterDir);

  runOcel(adapterDir, ["build"]);
  planFirst(adapterDir, ref);
  runOcel(adapterDir, ["preview", "up", "--ref", ref, "--prebuilt"]);

  const resultPath = join(appDir, DEPLOY_RESULT_FILE);
  if (!existsSync(resultPath)) {
    throw new Error(`${resultPath} was not written; the deploy reported success but produced no result`);
  }
  const result = JSON.parse(readFileSync(resultPath, "utf8"));
  const { resolved, unattributed } = resolveAppURLs(result, { slug, pointer: ref });
  return [
    ...resolved.map((entry) => `${entry.app} ${entry.framework} ${entry.url ?? "(no url)"}`),
    ...unattributed.map((url) => `(unattributed) ${url}`),
  ];
}

function planFirst(adapterDir, ref) {
  const logPath = join(appDir, BUILD_LOG_FILE);
  const before = existsSync(logPath) ? readFileSync(logPath, "utf8").length : 0;
  runOcel(adapterDir, ["preview", "up", "--ref", ref, "--prebuilt", "--dry"]);
  const problems = planProblems(readFileSync(logPath, "utf8").slice(before), {
    resultWritten: existsSync(join(appDir, DEPLOY_RESULT_FILE)),
  });
  if (problems.length > 0) {
    throw new Error(`--dry did not stay a plan:\n  ${problems.join("\n  ")}`);
  }
  console.error("[ocel-e2e-node] --dry planned this preview and changed nothing");
}

function runOcel(adapterDir, args) {
  run(`ocel ${args[0]}`, process.execPath, [join(adapterDir, "packages", "ocel", "bin", "run.js"), ...args]);
}

function run(label, command, args) {
  const remaining = deadline - Date.now();
  if (remaining <= 0) {
    throw new Error(`the shared build+deploy budget was exhausted before ${label} started`);
  }
  const log = openSync(join(appDir, BUILD_LOG_FILE), "a");
  try {
    const res = spawnSync(command, args, {
      cwd: appDir,
      env: CHILD_ENV,
      stdio: ["ignore", log, log],
      timeout: remaining,
      killSignal: "SIGTERM",
    });
    if (res.error) {
      throw new Error(`${label}: ${res.error.message}`);
    }
    if (res.signal) {
      throw new Error(`${label} timed out and was killed with ${res.signal}`);
    }
    if (res.status !== 0) {
      throw new Error(`${label} exited with ${res.status}`);
    }
  } finally {
    closeSync(log);
  }
}

function echoLogTail() {
  const path = join(appDir, BUILD_LOG_FILE);
  if (!existsSync(path)) {
    return;
  }
  console.error(`[ocel-e2e-node] last ${FAILURE_LOG_LINES} lines of ${BUILD_LOG_FILE}:`);
  console.error(tail(readFileSync(path, "utf8"), FAILURE_LOG_LINES));
}

function required(name) {
  const value = process.env[name];
  if (!value) {
    throw new Error(`${name} is not set`);
  }
  return value;
}
