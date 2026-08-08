#!/usr/bin/env node
// Builds and deploys the express smoke app, prints **only** the deployment
// URL to stdout, everything else to stderr or files in the temp app
// directory it creates. Unlike scripts/e2e-next/deploy.mjs, nothing external
// drives this — there is no adapter compatibility harness for a plain node
// app to plug into, so this script both builds the temp app's identity (a
// fresh directory per run) and drives the deploy end to end.
//
// Each run gets its own Ocel PROJECT (projectSlugForApp), deployed as one
// persistent preview inside it — same isolation reasoning as e2e-next's, see
// that file's own header.

import { spawnSync } from "node:child_process";
import { cpSync, existsSync, mkdtempSync, openSync, readFileSync, closeSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

import { linkSidecar } from "@ocel-scripts/e2e-shared/sidecar.mjs";

import { APP_NAME, BUILD_LOG_FILE, DEPLOY_RESULT_FILE, STATE_FILE, deployURL, projectSlugForApp, renderOcelConfig, tail } from "./lib.mjs";

const DEFAULT_TIMEOUT_MS = 15 * 60 * 1000;
const deadline = Date.now() + (Number(process.env.OCEL_E2E_DEPLOY_TIMEOUT_MS) || DEFAULT_TIMEOUT_MS);

const FAILURE_LOG_LINES = 200;

// OCEL_BYTECODE_CACHE=1 explicitly, same reasoning as e2e-next's deploy.mjs:
// the deploy-side gate (cloud/aws/deploy/bytecode.go) is off by default, and
// this harness's whole point is proving the two legs it turns on.
const CHILD_ENV = { ...process.env, OCEL_BYTECODE_CACHE: "1" };

const HERE = dirname(fileURLToPath(import.meta.url));
const smokeAppDir = join(HERE, "smoke-app");
let appDir;

try {
  process.stdout.write(deploy() + "\n");
} catch (err) {
  console.error(`[ocel-e2e-node] deploy failed: ${err.message}`);
  echoLogTail();
  process.exit(1);
}

function deploy() {
  const adapterDir = required("ADAPTER_DIR");
  const previewDomain = process.env.OCEL_E2E_PREVIEW_DOMAIN || "";
  const sidecarDir = required("OCEL_E2E_SIDECAR_DIR");

  appDir = mkdtempSync(join(tmpdir(), "ocel-e2e-node-"));
  cpSync(smokeAppDir, appDir, { recursive: true });
  console.error(`[ocel-e2e-node] staged smoke app in ${appDir}`);

  const slug = projectSlugForApp(appDir);
  // Persisted first, before anything is provisioned: cleanup has to be able to
  // tear down a deploy that failed halfway through.
  writeFileSync(
    join(appDir, STATE_FILE),
    JSON.stringify({ slug, appName: APP_NAME, previewDomain, startedAt: Date.now() }, null, 2) + "\n",
  );
  console.error(`[ocel-e2e-node] project ${slug} in ${appDir}`);

  writeFileSync(join(appDir, "ocel.config.ts"), renderOcelConfig({ slug, previewDomain }));
  ensureDeps();
  linkSidecar(appDir, sidecarDir);

  // Build and deploy are separate CLI runs so a build failure is reported as
  // one, rather than as a failed deploy — same split as e2e-next's, though a
  // plain node app's `build` traces its entrypoint rather than running a user
  // build script (cli/platform/src/builder/trace.ts): no package.json "build"
  // script is required or injected here.
  runOcel(adapterDir, ["build"]);
  runOcel(adapterDir, ["preview", "up", "--name", slug, "--prebuilt"]);

  const resultPath = join(appDir, DEPLOY_RESULT_FILE);
  if (!existsSync(resultPath)) {
    throw new Error(`${resultPath} was not written; the deploy reported success but produced no result`);
  }
  return deployURL(JSON.parse(readFileSync(resultPath, "utf8")));
}

// ensureDeps installs the smoke app's one real dependency (express) from the
// registry — nothing here comes from the workspace, since a deployed app has
// to resolve everything on its own the way a real user's app would. `ocel`
// and `@ocel/provider-aws` are deliberately NOT installed this way; they are
// symlinked in by linkSidecar from the prebuilt sidecar instead.
function ensureDeps() {
  if (existsSync(join(appDir, "node_modules", "express"))) {
    return;
  }
  console.error("[ocel-e2e-node] no node_modules/express; installing dependencies");
  run("pnpm install", "pnpm", ["install", "--prefer-offline"]);
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
  if (!appDir) return;
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
