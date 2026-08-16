#!/usr/bin/env node

import { execFileSync } from "node:child_process";
import { cpSync, existsSync, mkdirSync, mkdtempSync, rmSync, symlinkSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { fileURLToPath } from "node:url";

import { LOG_PREFIX, STATE_FILE, projectSlugForRun, renderSstConfig } from "./lib.mjs";

const here = fileURLToPath(new URL(".", import.meta.url));

const adapterDir = process.env.ADAPTER_DIR;
if (!adapterDir) {
  console.error(`${LOG_PREFIX} ADAPTER_DIR is not set; it must point at this repository`);
  process.exit(2);
}

const region = process.env.AWS_REGION || process.env.E2E_AWS_REGION;
if (!region) {
  console.error(`${LOG_PREFIX} AWS_REGION is not set; the staged SST app names the region it deploys into`);
  process.exit(2);
}

const projectDir = process.env.OCEL_E2E_SST_PROJECT_DIR;
if (!projectDir) {
  console.error(
    `${LOG_PREFIX} OCEL_E2E_SST_PROJECT_DIR is not set; it must name the staged ocel project that holds ocel.config.ts, which is where the adapter runs \`ocel link\`. Run stage-ocel-app.mjs first.`,
  );
  process.exit(2);
}
if (!existsSync(join(projectDir, "ocel.config.ts"))) {
  console.error(`${LOG_PREFIX} ${projectDir} holds no ocel.config.ts, so \`ocel link\` has no project to publish into`);
  process.exit(2);
}

execFileSync("pnpm", ["--filter", "@ocel/sst", "build"], { cwd: adapterDir, stdio: "inherit" });
if (!existsSync(join(adapterDir, "packages", "sst", "dist", "index.js"))) {
  console.error(`${LOG_PREFIX} @ocel/sst built no dist/index.js, and that is what the staged app imports`);
  process.exit(2);
}

const root = process.env.OCEL_E2E_SST_STAGE_ROOT || tmpdir();
const staged = mkdtempSync(join(root, "ocel-e2e-sst-"));
cpSync(join(here, "sst-app"), staged, { recursive: true });

const project = projectSlugForRun();
writeFileSync(
  join(staged, "sst.config.ts"),
  renderSstConfig({ app: project, projectDir, region }),
);
writeFileSync(
  join(staged, STATE_FILE),
  JSON.stringify({ app: project, project, projectDir, region, staged }, null, 2),
);

execFileSync("npm", ["install", "--omit=dev"], { cwd: staged, stdio: "inherit" });

const modules = join(staged, "node_modules", "@ocel");
mkdirSync(modules, { recursive: true });
rmSync(join(modules, "sst"), { force: true, recursive: true });
symlinkSync(join(adapterDir, "packages", "sst"), join(modules, "sst"), "dir");

console.error(`${LOG_PREFIX} staged ${staged} to publish into ${projectDir} as project ${project}`);
console.log(staged);
if (process.argv[2]) {
  writeFileSync(process.argv[2], `STAGED=${staged}\nOCEL_E2E_SST_PROJECT=${project}\n`, { flag: "a" });
}
