#!/usr/bin/env node
// NEXT_TEST_DEPLOY_SCRIPT_PATH for the Next.js deployment-adapter compatibility
// harness. Runs with cwd set to the harness's isolated temp app; prints only the
// deployment URL to stdout, everything else to stderr or files in cwd.
//
// Each temp app gets its own preview environment AND its own declared app name
// (previewSlug): that gives it its own Cloudflare worker script and S3 asset
// prefix, so suites running concurrently cannot overwrite each other's assets.

import { spawnSync } from "node:child_process";
import { existsSync, lstatSync, mkdirSync, openSync, readFileSync, closeSync, rmSync, symlinkSync, writeFileSync } from "node:fs";
import { join } from "node:path";

import {
  BUILD_LOG_FILE,
  STATE_FILE,
  deployURL,
  previewSlug,
  projectSlug,
  renderOcelConfig,
  tail,
  withBuildScript,
} from "./lib.mjs";

// How long a single deploy may take before it is killed. A hung deploy must fail
// its own suite rather than burn the job's whole timeout.
const DEFAULT_TIMEOUT_MS = 25 * 60 * 1000;

// Lines of the deploy log echoed to stderr when the deploy fails.
const FAILURE_LOG_LINES = 200;

const appDir = process.cwd();

try {
  process.stdout.write(deploy() + "\n");
} catch (err) {
  console.error(`[ocel-e2e] deploy failed: ${err.message}`);
  echoLogTail();
  process.exit(1);
}

function deploy() {
  const adapterDir = required("ADAPTER_DIR");
  const projectId = required("OCEL_E2E_PROJECT_ID");
  const slugForProject = process.env.OCEL_E2E_PROJECT_SLUG || projectSlug(projectId);
  const previewDomain = process.env.OCEL_E2E_PREVIEW_DOMAIN || "";
  const sidecarDir = required("OCEL_E2E_SIDECAR_DIR");

  const slug = previewSlug({ runId: process.env.GITHUB_RUN_ID, dir: process.env.NEXT_TEST_DIR || appDir });
  // Persisted first, before anything is provisioned: cleanup has to be able to
  // tear down a deploy that failed halfway through.
  writeFileSync(
    join(appDir, STATE_FILE),
    JSON.stringify({ slug, appName: slug, projectId, previewDomain, startedAt: Date.now() }, null, 2) + "\n",
  );
  console.error(`[ocel-e2e] preview ${slug} in ${appDir}`);

  writeFileSync(
    join(appDir, "ocel.config.ts"),
    renderOcelConfig({ slug: slugForProject, projectId, previewDomain, appName: slug }),
  );
  linkSidecar(sidecarDir);
  ensureBuildScript();

  runPreviewUp(adapterDir, slug);

  const resultPath = join(appDir, ".ocel", "deploy-result.json");
  if (!existsSync(resultPath)) {
    throw new Error(`${resultPath} was not written; the deploy reported success but produced no result`);
  }
  return deployURL(JSON.parse(readFileSync(resultPath, "utf8")));
}

// linkSidecar points the temp app at the prebuilt @ocel packages. Only the
// @ocel scope is linked: the harness owns the rest of node_modules (notably its
// isolated `next`), and replacing the directory would break the app it built.
//
// `@ocel/sdk` (the bundled ocel.config.ts imports it) and
// `@ocel/provider-aws-<platform>` (providerlocator require.resolve's it from the
// project dir) are the two that must resolve from here.
function linkSidecar(sidecarDir) {
  const target = join(sidecarDir, "node_modules", "@ocel");
  if (!existsSync(target)) {
    throw new Error(`sidecar has no @ocel packages at ${target}`);
  }
  const modules = join(appDir, "node_modules");
  mkdirSync(modules, { recursive: true });
  const link = join(modules, "@ocel");
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

// ensureBuildScript guarantees the `build` script buildNext requires (it throws
// without one). The harness writes one itself in deploy mode; this is the
// backstop for a fixture whose package.json it leaves alone.
function ensureBuildScript() {
  const path = join(appDir, "package.json");
  const pkg = JSON.parse(readFileSync(path, "utf8"));
  const patched = withBuildScript(pkg);
  if (patched !== pkg) {
    writeFileSync(path, JSON.stringify(patched, null, 2) + "\n");
    console.error(`[ocel-e2e] added a "build" script to package.json`);
  }
}

// runPreviewUp drives the npm launcher (which exports the adapter/builder/worker
// paths the Go CLI reads) with every byte of output redirected to the build log:
// the harness reads this process's stdout as the deployment URL and nothing else.
function runPreviewUp(adapterDir, slug) {
  const log = openSync(join(appDir, BUILD_LOG_FILE), "a");
  try {
    const res = spawnSync(
      process.execPath,
      [join(adapterDir, "packages", "ocel", "bin", "run.js"), "preview", "up", "--name", slug],
      {
        cwd: appDir,
        stdio: ["ignore", log, log],
        timeout: Number(process.env.OCEL_E2E_DEPLOY_TIMEOUT_MS) || DEFAULT_TIMEOUT_MS,
        killSignal: "SIGTERM",
      },
    );
    if (res.error) {
      throw new Error(`ocel preview up: ${res.error.message}`);
    }
    if (res.signal) {
      throw new Error(`ocel preview up timed out and was killed with ${res.signal}`);
    }
    if (res.status !== 0) {
      throw new Error(`ocel preview up exited with ${res.status}`);
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
