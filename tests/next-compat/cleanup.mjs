#!/usr/bin/env node

import { spawnSync } from "node:child_process";
import { existsSync, readFileSync, writeFileSync } from "node:fs";
import { join } from "node:path";

import { printFunctionLogs } from "./aws.mjs";
import {
  DEPLOY_REPORT_FILE,
  isProductionTarget,
  ocelBinary,
  previewNameForApp,
  projectSlugForApp,
  readCompatTarget,
  renderOcelConfig,
  requireNamespace,
  SKIP_CHECKS_ENV,
  STATE_FILE,
  tailLogEvents,
} from "./lib.mjs";

const TEARDOWN_TIMEOUT_MS = 20 * 60 * 1000;

const FALLBACK_LOG_WINDOW_MS = 60 * 60 * 1000;

const MAX_EVENTS_SCANNED = 20_000;

const MAX_TAIL_LINES = 300;

const MAX_TAIL_BYTES = 64 * 1024;

requireNamespace();

const appDir = process.cwd();
const adapterDir = process.env.ADAPTER_DIR;
if (!adapterDir) {
  console.error("[ocel-e2e] cleanup cannot run: ADAPTER_DIR is not set");
  process.exit(1);
}

const target = readCompatTarget();
const production = isProductionTarget(target);
const { slug, name, startedAt } = resolveIdentity();
if (!target.gcp) {
  printTestWindowLogs();
}
ensureConfig(slug);
const command = production ? ["destroy", "production", "--yes"] : ["preview", "rm", name, "--yes"];
console.error(
  production
    ? `[ocel-e2e] destroying production of project ${slug}`
    : `[ocel-e2e] removing preview ${name} from project ${slug}`,
);

const res = spawnSync(process.execPath, [ocelBinary(adapterDir), ...command], {
  cwd: appDir,
  stdio: ["ignore", "inherit", "inherit"],
  timeout: TEARDOWN_TIMEOUT_MS,
  env: { ...process.env, ...SKIP_CHECKS_ENV },
});

if (res.error || res.signal || res.status !== 0) {
  const why =
    res.error?.message ?? (res.signal ? `killed with ${res.signal}` : `exited with ${res.status}`);
  console.error(
    `[ocel-e2e] TEARDOWN FAILED for ${production ? "production" : `preview ${name}`} of project ${slug}: ${why}\n` +
      `[ocel-e2e] its services and stacks are still live; remove them by running ` +
      `\`ocel ${command.join(" ")}\` from a directory whose ocel.config.ts ` +
      `declares slug: "${slug}", or take the whole project with ` +
      `\`node tests/next-compat/project-teardown.mjs ${slug}\``,
  );
  process.exit(1);
}

console.error(
  production ? `[ocel-e2e] production of ${slug} destroyed` : `[ocel-e2e] preview ${name} removed`,
);

function resolveIdentity() {
  let state = {};
  try {
    state = JSON.parse(readFileSync(join(appDir, STATE_FILE), "utf8")) ?? {};
  } catch {
    console.error(
      `[ocel-e2e] no readable ${STATE_FILE}; re-deriving the project slug and preview name`,
    );
  }
  return {
    slug: state.slug || projectSlugForApp(appDir, target),
    name: state.name || previewNameForApp(appDir),
    startedAt: Number(state.startedAt) || Date.now() - FALLBACK_LOG_WINDOW_MS,
  };
}

function printTestWindowLogs() {
  console.log(
    `=== lambda logs from deploy to teardown (the last ${MAX_TAIL_LINES} lines or ${MAX_TAIL_BYTES} bytes per function) ===`,
  );
  let storagePrefix;
  try {
    storagePrefix = JSON.parse(readFileSync(join(appDir, DEPLOY_REPORT_FILE), "utf8"))?.apps?.[0]
      ?.storagePrefix;
  } catch {}
  printFunctionLogs({
    slug,
    storagePrefix,
    startTime: startedAt,
    endTime: Date.now(),
    query: ["--max-items", String(MAX_EVENTS_SCANNED)],
    toLines: (events) => [
      ...(events.length >= MAX_EVENTS_SCANNED
        ? [
            `(the window holds more than ${MAX_EVENTS_SCANNED} events; these are the last of the first ${MAX_EVENTS_SCANNED})`,
          ]
        : []),
      ...tailLogEvents(events, { maxLines: MAX_TAIL_LINES, maxBytes: MAX_TAIL_BYTES }),
    ],
  });
}

function ensureConfig(slug) {
  const path = join(appDir, "ocel.config.ts");
  if (existsSync(path)) {
    return;
  }
  console.error(`[ocel-e2e] no ocel.config.ts; re-rendering it for project ${slug}`);
  writeFileSync(path, renderOcelConfig({ slug, target }));
}
