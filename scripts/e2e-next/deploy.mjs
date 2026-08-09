#!/usr/bin/env node
// NEXT_TEST_DEPLOY_SCRIPT_PATH for the Next.js deployment-adapter compatibility
// harness. Runs with cwd set to the harness's isolated temp app; prints only the
// deployment URL to stdout, everything else to stderr or files in cwd.
//
// Each temp app gets its own Ocel PROJECT (projectSlug), deployed as one
// persistent preview inside it. The project namespaces the Pulumi stacks, the
// deployments store, the asset prefixes and the Cloudflare worker scripts and
// routes, so suites running concurrently contend over nothing at all.

import { spawnSync } from "node:child_process";
import { existsSync, lstatSync, mkdirSync, openSync, readFileSync, closeSync, rmSync, symlinkSync, writeFileSync } from "node:fs";
import { join } from "node:path";

import {
  APP_NAME,
  BUILD_LOG_FILE,
  DEPLOY_RESULT_FILE,
  STATE_FILE,
  deployURL,
  projectSlugForApp,
  renderOcelConfig,
  tail,
  withBuildScript,
  withPinnedTypeScript,
} from "./lib.mjs";

// How long building and deploying one app may take, in total, before it is
// killed. A hung deploy must fail its own suite rather than burn the job's whole
// timeout, so the budget is shared across every command this script runs rather
// than granted to each: two runs must not cost twice the wall clock one used to.
const DEFAULT_TIMEOUT_MS = 25 * 60 * 1000;

const deadline = Date.now() + (Number(process.env.OCEL_E2E_DEPLOY_TIMEOUT_MS) || DEFAULT_TIMEOUT_MS);

// Lines of the deploy log echoed to stderr when the deploy fails.
const FAILURE_LOG_LINES = 200;

const appDir = process.cwd();

// The harness only injects test mode on its own Vercel and local paths, never on
// the custom-script one. Without it `next build` skips the shim that sets
// `__NEXT_TEST_MODE`, so `window.__NEXT_HYDRATED` never fires and every page load
// in the suite waits out next-webdriver's 10-second fallback.
const CHILD_ENV = {
  ...process.env,
  NEXT_PRIVATE_TEST_MODE: "e2e",
  // Next's deploy suites assert Vercel's `x-vercel-cache`. The adapter records
  // the opt-in in the build's routing manifest and the edge stamps the alias
  // beside `x-ocel-cache`; nothing outside this harness sets it (ocelhq-6l0y).
  OCEL_E2E_VERCEL_CACHE_HEADER: "1",
  // Next transpiles a TypeScript next.config through SWC to commonjs and
  // requires it from a string, which cannot load a config with top-level await;
  // only with this set does transpile-config take its `await import()` path.
  // `next build --experimental-next-config-strip-types` is what normally sets
  // it, and the build script here is the fixture's own plain `next build`.
  //
  // It reaches `next build` by inheritance: the Go CLI composes the builder's
  // environment from os.Environ() (cli/internal/appbuilder), and buildNext
  // spawns the build script under process.env (cli/platform/src/builder/next.ts).
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
  const previewDomain = process.env.OCEL_E2E_PREVIEW_DOMAIN || "";
  const sidecarDir = required("OCEL_E2E_SIDECAR_DIR");

  const slug = projectSlugForApp(appDir);
  // Persisted first, before anything is provisioned: cleanup has to be able to
  // tear down a deploy that failed halfway through.
  writeFileSync(
    join(appDir, STATE_FILE),
    JSON.stringify({ slug, appName: APP_NAME, previewDomain, startedAt: Date.now() }, null, 2) + "\n",
  );
  console.error(`[ocel-e2e] project ${slug} in ${appDir}`);

  writeFileSync(join(appDir, "ocel.config.ts"), renderOcelConfig({ slug, previewDomain }));
  // Before ensureDeps: the TypeScript pin is only worth anything if it is in
  // package.json when pnpm resolves it.
  patchPackageJson();
  ensureDeps();
  linkSidecar(sidecarDir);

  // Build and deploy are separate CLI runs so a build failure is reported as
  // one, rather than as a failed deploy. `preview up --prebuilt` then ships the
  // .ocel/output this produced instead of building the app a second time.
  //
  // `--name` makes it a persistent preview: an ephemeral one resolves its
  // identity from a git ref, and the harness's temp app directory is not a repo.
  runOcel(adapterDir, ["build"]);
  runOcel(adapterDir, ["preview", "up", "--name", slug, "--prebuilt"]);

  const resultPath = join(appDir, DEPLOY_RESULT_FILE);
  if (!existsSync(resultPath)) {
    throw new Error(`${resultPath} was not written; the deploy reported success but produced no result`);
  }
  return deployURL(JSON.parse(readFileSync(resultPath, "utf8")));
}

// ensureDeps installs the temp app's dependencies when the harness left it
// without them: without a resolvable `next`, the build fails before Ocel does
// anything. It runs before linkSidecar because an install can rewrite
// node_modules, which would drop the `ocel` and `@ocel` symlinks.
function ensureDeps() {
  if (existsSync(join(appDir, "node_modules", ".bin", "next"))) {
    return;
  }
  console.error("[ocel-e2e] no node_modules/.bin/next; installing dependencies");
  run("pnpm install", "pnpm", ["install", "--prefer-offline"]);
}

// linkSidecar points the temp app at the prebuilt Ocel packages. Only `ocel`
// and the @ocel scope are linked: the harness owns the rest of node_modules
// (notably its isolated `next`), and replacing the directory would break the
// app it built.
//
// `ocel` (the bundled ocel.config.ts imports ocel/config) and
// `@ocel/provider-aws-<platform>` (providerlocator require.resolve's it from the
// project dir) are the two that must resolve from here.
function linkSidecar(sidecarDir) {
  const modules = join(appDir, "node_modules");
  mkdirSync(modules, { recursive: true });
  for (const name of ["ocel", "@ocel"]) {
    const target = join(sidecarDir, "node_modules", name);
    if (!existsSync(target)) {
      throw new Error(
        `sidecar has no ${name} package at ${target}. A sidecar packed before ` +
          `@ocel/sdk folded into the root ocel package carries only @ocel/*; ` +
          `repack it from the ocel and @ocel/provider-aws* tarballs — see ` +
          `"Repacking the sidecar" in scripts/e2e-next/README.md.`,
      );
    }
    const link = join(modules, name);
    if (existsSync(link) || isSymlink(link)) {
      rmSync(link, { recursive: true, force: true });
    }
    symlinkSync(target, link, "dir");
  }
}

function isSymlink(path) {
  try {
    return lstatSync(path).isSymbolicLink();
  } catch {
    return false;
  }
}

// patchPackageJson gives the fixture the `build` script buildNext requires
// (which the harness does not always leave one with) and pins its TypeScript.
function patchPackageJson() {
  const path = join(appDir, "package.json");
  const pkg = JSON.parse(readFileSync(path, "utf8"));
  const patched = withPinnedTypeScript(withBuildScript(pkg));
  if (patched !== pkg) {
    writeFileSync(path, JSON.stringify(patched, null, 2) + "\n");
    console.error("[ocel-e2e] patched package.json (build script, typescript pin)");
  }
}

// runOcel drives the npm launcher (which execs the Go binary) with every byte
// of output appended to the build log: the harness reads this process's stdout
// as the deployment URL and nothing else, and logs.mjs replays the whole log
// afterward.
function runOcel(adapterDir, args) {
  run(`ocel ${args[0]}`, process.execPath, [join(adapterDir, "packages", "ocel", "bin", "run.js"), ...args]);
}

// run spends what is left of the shared budget on one command, appending its
// output to the build log.
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
