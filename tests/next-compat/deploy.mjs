#!/usr/bin/env node

import { spawnSync } from "node:child_process";
import {
  existsSync,
  openSync,
  readFileSync,
  readdirSync,
  closeSync,
  rmSync,
  writeFileSync,
} from "node:fs";
import { join } from "node:path";

import {
  APP_NAME,
  BUILD_LOG_FILE,
  DEPLOY_RESULT_FILE,
  SKIP_DRIFT_CHECK_ENV,
  STATE_FILE,
  deployURL,
  planProblems,
  previewRefForApp,
  projectSlugForRun,
  renderOcelConfig,
  tail,
  withBuildScript,
  withPinnedTypeScript,
} from "./lib.mjs";
import { linkSidecar } from "./sidecar.mjs";

const DEFAULT_TIMEOUT_MS = 25 * 60 * 1000;

const deadline = Date.now() + (Number(process.env.OCEL_E2E_DEPLOY_TIMEOUT_MS) || DEFAULT_TIMEOUT_MS);

const FAILURE_LOG_LINES = 200;

const HARNESS_TEST_FILE = /\.(test|spec)\.[cm]?[jt]sx?$/;

const NOT_APP_SOURCE = new Set(["node_modules", ".next", ".ocel", ".git"]);

const appDir = process.cwd();

const CHILD_ENV = {
  ...process.env,
  NEXT_PRIVATE_TEST_MODE: "e2e",
  OCEL_E2E_VERCEL_CACHE_HEADER: "1",
  OCEL_EDGE_OBSERVABILITY: "off",
  ...SKIP_DRIFT_CHECK_ENV,
  ...(hasTypeScriptNextConfig() ? { __NEXT_NODE_NATIVE_TS_LOADER_ENABLED: "true" } : {}),
};

function hasTypeScriptNextConfig() {
  return ["next.config.ts", "next.config.mts"].some((name) => existsSync(join(appDir, name)));
}

try {
  process.stdout.write(deploy() + "\n");
} catch (err) {
  console.error(`[ocel-e2e] deploy failed: ${err.message}`);
  echoLogTail();
  process.exit(1);
}

function deploy() {
  const adapterDir = required("ADAPTER_DIR");
  const sidecarDir = required("OCEL_E2E_SIDECAR_DIR");

  const slug = projectSlugForRun();
  const ref = previewRefForApp(appDir);
  writeFileSync(
    join(appDir, STATE_FILE),
    JSON.stringify({ slug, ref, appName: APP_NAME, startedAt: Date.now() }, null, 2) + "\n",
  );
  console.error(`[ocel-e2e] preview ${ref} of project ${slug} in ${appDir}`);

  writeFileSync(join(appDir, "ocel.config.ts"), renderOcelConfig({ slug }));
  dropHarnessTests();
  patchPackageJson();
  ensureDeps();
  linkSidecar(appDir, sidecarDir);

  runOcel(adapterDir, ["build"]);
  planFirst(adapterDir, ref);
  runOcel(adapterDir, ["preview", "up", "--ref", ref, "--prebuilt"]);

  const resultPath = join(appDir, DEPLOY_RESULT_FILE);
  if (!existsSync(resultPath)) {
    throw new Error(`${resultPath} was not written; the deploy reported success but produced no result`);
  }
  return deployURL(JSON.parse(readFileSync(resultPath, "utf8")));
}

function dropHarnessTests(dir = appDir) {
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    if (entry.isDirectory()) {
      if (!NOT_APP_SOURCE.has(entry.name)) dropHarnessTests(join(dir, entry.name));
    } else if (HARNESS_TEST_FILE.test(entry.name)) {
      rmSync(join(dir, entry.name));
    }
  }
}

function ensureDeps() {
  if (existsSync(join(appDir, "node_modules", ".bin", "next"))) {
    return;
  }
  const pkg = JSON.parse(readFileSync(join(appDir, "package.json"), "utf8"));
  if (/^npm@/.test(pkg.packageManager ?? "")) {
    console.error("[ocel-e2e] no node_modules/.bin/next; installing dependencies with npm");
    run("npm install", "npm", ["install", "--no-audit", "--no-fund", "--prefer-offline"]);
    return;
  }
  console.error("[ocel-e2e] no node_modules/.bin/next; installing dependencies");
  run("pnpm install", "pnpm", ["install", "--prefer-offline"]);
}

function patchPackageJson() {
  const path = join(appDir, "package.json");
  const pkg = JSON.parse(readFileSync(path, "utf8"));
  const patched = withPinnedTypeScript(withBuildScript(pkg));
  if (patched !== pkg) {
    writeFileSync(path, JSON.stringify(patched, null, 2) + "\n");
    console.error("[ocel-e2e] patched package.json (build script, typescript pin)");
  }
}

function planFirst(adapterDir, ref) {
  const logPath = join(appDir, BUILD_LOG_FILE);
  const before = existsSync(logPath) ? readFileSync(logPath, "utf8").length : 0;
  runOcel(adapterDir, ["preview", "up", "--ref", ref, "--prebuilt", "--dry"]);
  const planned = readFileSync(logPath, "utf8").slice(before);
  const listedFrom = readFileSync(logPath, "utf8").length;
  runOcel(adapterDir, ["preview", "ls"]);
  const problems = planProblems(planned, {
    resultWritten: existsSync(join(appDir, DEPLOY_RESULT_FILE)),
    listed: readFileSync(logPath, "utf8").slice(listedFrom),
    ref,
  });
  if (problems.length > 0) {
    throw new Error(`--dry did not stay a plan:\n  ${problems.join("\n  ")}`);
  }
  console.error("[ocel-e2e] --dry planned this preview and changed nothing");
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
  console.error(`[ocel-e2e] last ${FAILURE_LOG_LINES} lines of ${BUILD_LOG_FILE}:`);
  console.error(tail(readFileSync(path, "utf8"), FAILURE_LOG_LINES));
}

function required(name) {
  const value = process.env[name];
  if (!value) {
    throw new Error(`${name} is not set`);
  }
  return value;
}
