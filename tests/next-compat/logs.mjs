#!/usr/bin/env node

import { existsSync, readdirSync, readFileSync, statSync } from "node:fs";
import { join } from "node:path";

import { printFunctionLogs } from "./aws.mjs";
import { BUILD_LOG_FILE, DEPLOY_REPORT_FILE, markerLines, STATE_FILE } from "./lib.mjs";

const DEFAULT_LOG_WINDOW_MS = 60 * 60 * 1000;

const MAX_EVENTS_PER_GROUP = 200;

const appDir = process.cwd();
const state = readJSON(join(appDir, STATE_FILE)) ?? {};
const result = readJSON(join(appDir, DEPLOY_REPORT_FILE)) ?? {};

for (const line of markerLines({ buildId: readFrameworkBuildID(), deploymentId: readBuildID() })) {
  console.log(line);
}

replay(BUILD_LOG_FILE, join(appDir, BUILD_LOG_FILE));
replayRuns(join(appDir, ".ocel", "runs"));
printLambdaLogs();

function readFrameworkBuildID() {
  const path = join(appDir, ".next", "BUILD_ID");
  if (existsSync(path)) {
    return readFileSync(path, "utf8").trim();
  }
  return result.apps?.[0]?.frameworkBuildId;
}

function readBuildID() {
  return result.apps?.[0]?.buildId;
}

function replay(label, path) {
  console.log(`=== ${label} ===`);
  if (!existsSync(path)) {
    console.log(`(no ${label})`);
    return;
  }
  console.log(readFileSync(path, "utf8"));
}

function replayRuns(dir) {
  const logs = existsSync(dir)
    ? readdirSync(dir)
        .filter((name) => name.endsWith(".ndjson"))
        .map((name) => join(dir, name))
        .sort((a, b) => statSync(a).mtimeMs - statSync(b).mtimeMs)
    : [];
  if (logs.length === 0) {
    console.log("=== ocel runs ===\n(no ocel run log)");
  }
  for (const path of logs) {
    replay(`ocel run ${path.slice(dir.length + 1)}`, path);
  }
}

function printLambdaLogs() {
  console.log("=== lambda logs ===");
  printFunctionLogs({
    slug: state.slug,
    storagePrefix: result.apps?.[0]?.storagePrefix,
    startTime: Number(state.startedAt) || Date.now() - DEFAULT_LOG_WINDOW_MS,
    query: ["--limit", String(MAX_EVENTS_PER_GROUP)],
    toLines: (events) =>
      events.map(
        (event) => `${new Date(event.timestamp).toISOString()} ${(event.message ?? "").trimEnd()}`,
      ),
  });
}

function readJSON(path) {
  try {
    return JSON.parse(readFileSync(path, "utf8"));
  } catch {
    return null;
  }
}
