#!/usr/bin/env node

import { execFileSync } from "node:child_process";
import { cpSync, mkdtempSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { fileURLToPath } from "node:url";

import {
  CONSUMER_APP,
  CONSUMER_STATE_FILE,
  LINK_NAME,
  LOG_PREFIX,
  projectSlugForRun,
  refuseUntilRePlumbed,
  renderOcelConfig,
} from "./lib.mjs";
import { assertToolchain, linkOcel } from "./toolchain.mjs";

refuseUntilRePlumbed();

const here = fileURLToPath(new URL(".", import.meta.url));

const adapterDir = process.env.ADAPTER_DIR;
if (!adapterDir) {
  console.error(`${LOG_PREFIX} ADAPTER_DIR is not set; it must point at this repository`);
  process.exit(2);
}

try {
  assertToolchain(adapterDir);
} catch (err) {
  console.error(`${LOG_PREFIX} ${err.message}`);
  process.exit(2);
}

const root = process.env.OCEL_E2E_SST_STAGE_ROOT || tmpdir();
const staged = mkdtempSync(join(root, "ocel-e2e-sst-consumer-"));
cpSync(join(here, "ocel-app"), staged, { recursive: true });

const slug = projectSlugForRun();
writeFileSync(join(staged, "ocel.config.ts"), renderOcelConfig({ slug }));
writeFileSync(
  join(staged, CONSUMER_STATE_FILE),
  JSON.stringify({ slug, app: CONSUMER_APP, link: LINK_NAME, staged }, null, 2),
);

execFileSync("npm", ["install", "--no-audit", "--no-fund", "--omit=dev"], {
  cwd: staged,
  stdio: "inherit",
});
linkOcel(staged, adapterDir);

console.error(`${LOG_PREFIX} staged ${staged} to consume ${LINK_NAME} as project ${slug}`);
console.log(staged);
if (process.argv[2]) {
  writeFileSync(process.argv[2], `CONSUMER_STAGED=${staged}\nOCEL_E2E_SST_PROJECT=${slug}\n`, {
    flag: "a",
  });
}
