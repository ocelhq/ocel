#!/usr/bin/env node

import { execFileSync } from "node:child_process";
import { cpSync, existsSync, mkdirSync, mkdtempSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

import {
  CONSUMER_APP,
  CONSUMER_STATE_FILE,
  LINK_NAME,
  LOG_PREFIX,
  PRODUCTION_DOMAIN_ENV_VAR,
  TRANSFORM_MODULE,
  productionHostFor,
  projectSlugForRun,
  renderOcelConfig,
} from "./lib.mjs";
import { assertToolchain, linkOcel } from "./toolchain.mjs";

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

const transformSource = join(adapterDir, "examples", "with-sst", TRANSFORM_MODULE);
if (!existsSync(transformSource)) {
  console.error(
    `${LOG_PREFIX} ${transformSource} is not there; this suite deploys the example's own transform module, and there is nothing else to deploy`,
  );
  process.exit(2);
}
mkdirSync(join(staged, dirname(TRANSFORM_MODULE)), { recursive: true });
cpSync(transformSource, join(staged, TRANSFORM_MODULE));

const slug = projectSlugForRun();
const host = productionHostFor(slug);
if (!host) {
  console.error(
    `${LOG_PREFIX} ${PRODUCTION_DOMAIN_ENV_VAR} is not set; a production deploy serves a hostname this project declares, so set it to a zone these credentials own (e.g. e2e.example.com) and run this again`,
  );
  process.exit(2);
}
writeFileSync(join(staged, "ocel.config.ts"), renderOcelConfig({ slug, host }));
writeFileSync(
  join(staged, CONSUMER_STATE_FILE),
  JSON.stringify({ slug, app: CONSUMER_APP, link: LINK_NAME, host, staged }, null, 2),
);

execFileSync("npm", ["install", "--no-audit", "--no-fund", "--omit=dev"], {
  cwd: staged,
  stdio: "inherit",
});
linkOcel(staged, adapterDir);

console.error(`${LOG_PREFIX} staged ${staged} to consume ${LINK_NAME} as project ${slug}`);
console.log(staged);
if (process.argv[2]) {
  writeFileSync(
    process.argv[2],
    `CONSUMER_STAGED=${staged}\nOCEL_E2E_SST_PROJECT=${slug}\nOCEL_E2E_SST_PROJECT_DIR=${staged}\n`,
    { flag: "a" },
  );
}
