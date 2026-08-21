#!/usr/bin/env node

import { spawnSync } from "node:child_process";
import { mkdirSync, mkdtempSync, readFileSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { pathToFileURL } from "node:url";

import { projectSlugForRun, renderOcelConfig, withoutSkipDriftChecks } from "./lib.mjs";
import { linkSidecar } from "./sidecar.mjs";

const TIERS = ["bootstrap", "deploy"];
const MODES = ["policy", "policy-parts", "run"];

const MANAGED_POLICY_CHARS = 6144;
const MANAGED_SESSION_POLICIES = 10;

const POLICY_TIMEOUT_MS = 5 * 60 * 1000;
const BOOTSTRAP_TIMEOUT_MS = 30 * 60 * 1000;
const STAGE_TIMEOUT_MS = 30 * 60 * 1000;
const DEPLOY_TIMEOUT_MS = 30 * 60 * 1000;

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  const [mode, tier, dir] = process.argv.slice(2);
  if (!TIERS.includes(tier) || !MODES.includes(mode)) {
    console.error(`usage: credential-tier.mjs ${MODES.join("|")} ${TIERS.join("|")} [dir]`);
    process.exit(2);
  }
  if (mode === "policy") {
    process.stdout.write(printPolicy(tier) + "\n");
  } else if (mode === "policy-parts") {
    if (!dir) {
      console.error("policy-parts needs a directory to write the parts into");
      process.exit(2);
    }
    process.stdout.write(writePolicyParts(tier, dir).join("\n") + "\n");
  } else {
    runTier(tier);
  }
}

export function policyParts(document) {
  const policy = JSON.parse(document);
  const render = (statements) => JSON.stringify({ Version: policy.Version, Statement: statements });
  const parts = [];
  let current = [];
  for (const statement of policy.Statement) {
    const alone = render([statement]);
    if (alone.length > MANAGED_POLICY_CHARS) {
      throw new Error(`one statement renders to ${alone.length} characters, past the ${MANAGED_POLICY_CHARS} a managed policy carries: ${alone}`);
    }
    if (current.length > 0 && render([...current, statement]).length > MANAGED_POLICY_CHARS) {
      parts.push(render(current));
      current = [];
    }
    current.push(statement);
  }
  if (current.length > 0) {
    parts.push(render(current));
  }
  if (parts.length > MANAGED_SESSION_POLICIES) {
    throw new Error(`the document splits into ${parts.length} managed policies, past the ${MANAGED_SESSION_POLICIES} a role session may carry`);
  }
  return parts;
}

export function writePolicyParts(tier, dir) {
  mkdirSync(dir, { recursive: true });
  return policyParts(printPolicy(tier)).map((part, index) => {
    const path = join(dir, `${tier}-${index}.json`);
    writeFileSync(path, part);
    return path;
  });
}

export function printPolicy(tier) {
  const res = ocel(["bootstrap", "--print-policy", tier], {
    cwd: configuredDir(`policy-${tier}`),
    stdio: ["ignore", "pipe", "inherit"],
    timeout: POLICY_TIMEOUT_MS,
  });
  const document = res.stdout.toString().trim();
  if (!document.startsWith("{")) {
    throw new Error(`ocel bootstrap --print-policy ${tier} wrote no policy document, only: ${document || "(nothing)"}`);
  }
  return document;
}

function runTier(tier) {
  if (tier === "bootstrap") {
    ocel(["bootstrap", "--preview", "--yes"], {
      cwd: configuredDir("bootstrap"),
      stdio: ["ignore", "inherit", "inherit"],
      timeout: BOOTSTRAP_TIMEOUT_MS,
    });
    return;
  }
  const appDir = stageSmokeApp();
  run("deploy.mjs", process.execPath, [script("deploy.mjs")], {
    cwd: appDir,
    env: { ...process.env, NEXT_TEST_DIR: appDir },
    stdio: ["ignore", "inherit", "inherit"],
    timeout: DEPLOY_TIMEOUT_MS,
  });
}

function stageSmokeApp() {
  const outFile = join(mkdtempSync(join(tmpdir(), "ocel-e2e-tier-app-")), "app-dir");
  run("stage-smoke-app.mjs", process.execPath, [
    script("stage-smoke-app.mjs"),
    required("NEXTJS_DIR"),
    join(adapterDir(), "scripts", "e2e-next", "smoke-app"),
    outFile,
  ], {
    cwd: adapterDir(),
    env: process.env,
    stdio: ["ignore", "inherit", "inherit"],
    timeout: STAGE_TIMEOUT_MS,
  });
  return readFileSync(outFile, "utf8").trim();
}

function configuredDir(label) {
  const dir = mkdtempSync(join(tmpdir(), `ocel-e2e-tier-${label}-`));
  writeFileSync(join(dir, "ocel.config.ts"), renderOcelConfig({ slug: projectSlugForRun() }));
  linkSidecar(dir, required("OCEL_E2E_SIDECAR_DIR"));
  return dir;
}

function ocel(args, options) {
  return run(`ocel ${args.join(" ")}`, process.execPath, [
    join(adapterDir(), "packages", "ocel", "bin", "run.js"),
    ...args,
  ], { env: withoutSkipDriftChecks(process.env), ...options });
}

function run(label, command, args, options) {
  const res = spawnSync(command, args, options);
  if (res.error || res.signal || res.status !== 0) {
    const why = res.error?.message ?? (res.signal ? `killed with ${res.signal}` : `exited with ${res.status}`);
    console.error(`[ocel-e2e] ${label}: ${why}`);
    process.exit(1);
  }
  return res;
}

function script(name) {
  return join(adapterDir(), "scripts", "e2e-next", name);
}

function adapterDir() {
  return required("ADAPTER_DIR");
}

function required(name) {
  const value = process.env[name];
  if (!value) {
    throw new Error(`credential-tier needs ${name}`);
  }
  return value;
}
