#!/usr/bin/env node

import "./env-bootstrap.mjs";

import { spawnSync } from "node:child_process";
import { cpSync, existsSync, mkdirSync, readFileSync, rmSync } from "node:fs";
import { basename, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

import { APPS, PINNED, PLATFORMS, REGION, SAMPLES } from "../matrix.config.mjs";
import { deploymentProblems, driverProblems, expandMatrix } from "./contract.mjs";
import { awsUnreachable } from "./aws.mjs";
import { measure, defaultOps } from "./measure.mjs";
import { renderTable, resultsPayload, writeResults } from "./report.mjs";

const here = resolve(fileURLToPath(import.meta.url), "..");
const BENCH_ROOT = resolve(here, "..");
const APPS_DIR = join(BENCH_ROOT, "apps");
const STAGE_ROOT = join(BENCH_ROOT, ".staged");
const RESULTS_DIR = join(BENCH_ROOT, "results");

const NOT_STAGED = Object.freeze(new Set(["node_modules", ".git", ".ocel", ".sst", ".turbo", "cdk.out", "dist"]));

const DRY_RUN_DRIVER = "fake";

const options = parseArgv(process.argv.slice(2));

const startedAt = Date.now();
const stamp = new Date(startedAt).toISOString().replace(/[:.]/g, "-");
const outPath = options.out ? resolve(options.out) : join(RESULTS_DIR, `${stamp}.json`);

const matrix = expandMatrix({
  apps: APPS,
  platforms: options.dryRun ? PLATFORMS.map((platform) => ({ ...platform, driver: DRY_RUN_DRIVER })) : PLATFORMS,
  only: { frameworks: options.frameworks, platforms: options.platforms },
});

if (matrix.length === 0) {
  die(
    `no cells match --frameworks ${(options.frameworks ?? ["*"]).join(",")} --platforms ` +
      `${(options.platforms ?? ["*"]).join(",")}; known frameworks are ${APPS.map((app) => app.name).join(", ")} ` +
      `and known platforms are ${PLATFORMS.map((platform) => platform.id).join(", ")}`,
  );
}

let aborted = false;
process.on("SIGINT", () => {
  if (aborted) {
    log("second interrupt; leaving the in-flight cell to be torn down by hand");
    process.exit(130);
  }
  aborted = true;
  log("interrupted; tearing down the in-flight cell and writing what is measured so far");
});

const unreachable = options.dryRun ? null : awsUnreachable({ region: REGION });
if (unreachable) {
  log(`SKIPPING every cell: ${unreachable}`);
}

const cells = [];
for (const entry of matrix) {
  cells.push(await runCell(entry));
  if (aborted) break;
}

if (!process.env.BENCH_KEEP_STAGE) {
  rmSync(join(STAGE_ROOT, stamp), { recursive: true, force: true });
}

const payload = resultsPayload({
  cells,
  pinned: PINNED,
  region: REGION,
  samples: SAMPLES,
  startedAt,
  finishedAt: Date.now(),
  aborted,
});
writeResults(outPath, payload);

process.stdout.write(`\n${renderTable({ cells, pinned: PINNED, region: REGION, samples: SAMPLES })}\n`);
log(`raw samples written to ${outPath}`);

const failed = cells.filter((cell) => cell.status === "failed");
const skipped = cells.filter((cell) => cell.status === "skipped");
if (failed.length > 0) {
  log(`${failed.length} of ${cells.length} cell(s) failed: ${failed.map((cell) => cell.id).join(", ")}`);
}
if (skipped.length > 0) {
  log(`${skipped.length} of ${cells.length} cell(s) measured nothing at all and are SKIPPED, not passed`);
}
if (aborted) {
  log(`the run was interrupted after ${cells.length} of ${matrix.length} cell(s)`);
}
process.exit(aborted || failed.length > 0 || skipped.length > 0 ? 1 : 0);

async function runCell({ id, app, platform }) {
  const cell = {
    id,
    app: app.name,
    framework: app.framework,
    platform: platform.id,
    driver: platform.driver,
    env: platform.env,
    status: "pending",
    error: null,
    deploy: null,
    measurement: null,
  };
  const say = (message) => log(`${id}: ${message}`);

  if (unreachable) {
    return { ...cell, status: "skipped", error: unreachable };
  }

  const driver = await loadDriver(platform.driver);
  if (driver.problem) {
    say(`FAILED ${driver.problem}`);
    return { ...cell, status: "failed", error: driver.problem };
  }

  const staged = stage(app, platform, driver.module, say);
  if (staged.problem) {
    say(`FAILED ${staged.problem}`);
    return { ...cell, status: "failed", error: staged.problem };
  }
  cell.workdir = staged.dir;

  let deployment = null;
  let attempted = false;
  try {
    attempted = true;
    const deployStart = Date.now();
    say(`deploying`);
    deployment = await driver.module.deploy({
      app,
      platform,
      workdir: staged.dir,
      region: REGION,
      pinned: PINNED,
      env: platform.env,
      log: say,
    });
    const wallMs = Date.now() - deployStart;
    const problems = deploymentProblems(platform.id, deployment);
    if (problems.length > 0) {
      throw new Error(problems.join("; "));
    }
    cell.deploy = { wallMs, buildMs: deployment.buildMs, provisionMs: deployment.provisionMs };
    cell.deployment = { url: deployment.url, functionName: deployment.functionName };
    say(`deployed in ${(wallMs / 1000).toFixed(1)}s to ${deployment.url}`);

    if (aborted) {
      throw new Error("interrupted before the measurement ran");
    }

    cell.measurement = await measure({
      app,
      deployment,
      region: REGION,
      samples: SAMPLES,
      ops: driver.module.measurementOps ?? defaultOps,
      log: say,
      aborted: () => aborted,
    });
    cell.status = "measured";
    for (const warning of cell.measurement.warnings) say(`WARNING ${warning}`);
    for (const error of cell.measurement.errors.slice(0, 5)) say(`bad sample: ${error}`);
  } catch (err) {
    cell.status = "failed";
    cell.error = err.message;
    say(`FAILED ${err.message}`);
  } finally {
    if (attempted) {
      try {
        await driver.module.teardown({
          app,
          platform,
          workdir: staged.dir,
          region: REGION,
          deployment,
          log: say,
        });
        say(`torn down`);
      } catch (err) {
        cell.teardownError = err.message;
        say(`FAILED to tear down, resources may still be running: ${err.message}`);
      }
    }
    if (!process.env.BENCH_KEEP_STAGE) {
      rmSync(staged.dir, { recursive: true, force: true });
    }
  }
  return cell;
}

async function loadDriver(name) {
  const path = join(BENCH_ROOT, "platforms", name, "driver.mjs");
  if (!existsSync(path)) {
    return { problem: `no driver at ${path}; the ${name} platform has no driver module yet` };
  }
  let module;
  try {
    module = await import(path);
  } catch (err) {
    return { problem: `${path} could not be imported: ${err.message}` };
  }
  const problems = driverProblems(name, module);
  if (problems.length > 0) {
    return { problem: `${path} does not satisfy the driver contract: ${problems.join("; ")}` };
  }
  return { module };
}

function stage(app, platform, driver, say) {
  const source = join(APPS_DIR, app.name);
  const dir = join(STAGE_ROOT, stamp, `${app.name}-${platform.id}`);
  rmSync(dir, { recursive: true, force: true });
  mkdirSync(dir, { recursive: true });

  if (driver.needsSources === false) {
    return { dir };
  }
  if (!existsSync(source)) {
    return {
      problem: `no app at ${source}; the ${app.name} benchmark app has not been written yet`,
    };
  }

  cpSync(source, dir, {
    recursive: true,
    dereference: true,
    filter: (src) => src === source || !NOT_STAGED.has(basename(src)),
  });
  const problem = install(dir, say);
  return problem ? { problem } : { dir };
}

function install(dir, say) {
  const manifestPath = join(dir, "package.json");
  if (process.env.BENCH_SKIP_INSTALL || !existsSync(manifestPath)) return null;
  const manifest = JSON.parse(readFileSync(manifestPath, "utf8"));
  const count =
    Object.keys(manifest.dependencies ?? {}).length + Object.keys(manifest.devDependencies ?? {}).length;
  if (count === 0) return null;
  say(`installing ${count} dependency entr(ies) into the staged copy`);
  const res = spawnSync("npm", ["install", "--no-audit", "--no-fund"], {
    cwd: dir,
    stdio: ["ignore", "ignore", "pipe"],
    encoding: "utf8",
  });
  if (res.error || res.status !== 0) {
    return `npm install in ${dir} failed: ${res.error?.message ?? String(res.stderr).trim().split("\n").pop()}`;
  }
  return null;
}

function parseArgv(argv) {
  const parsed = { frameworks: null, platforms: null, out: null, dryRun: false };
  for (let i = 0; i < argv.length; i += 1) {
    const arg = argv[i];
    if (arg === "--dry-run") {
      parsed.dryRun = true;
    } else if (arg === "--frameworks" || arg === "--platforms" || arg === "--out") {
      const value = argv[i + 1];
      if (value === undefined || value.startsWith("--")) {
        die(`${arg} needs a value`);
      }
      i += 1;
      if (arg === "--out") parsed.out = value;
      else parsed[arg.slice(2)] = value.split(",").map((name) => name.trim()).filter(Boolean);
    } else {
      die(
        `unknown argument ${arg}; usage: run.mjs [--frameworks a,b] [--platforms x,y] [--out path.json] [--dry-run]`,
      );
    }
  }
  return parsed;
}

function log(message) {
  process.stdout.write(`[bench] ${message}\n`);
}

function die(message) {
  process.stderr.write(`[bench] ${message}\n`);
  process.exit(2);
}
