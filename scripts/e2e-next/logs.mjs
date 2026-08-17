#!/usr/bin/env node

import { execFileSync } from "node:child_process";
import { existsSync, readFileSync } from "node:fs";
import { join } from "node:path";

import { AWS_CLI_RETRY_ENV } from "./aws.mjs";
import { BUILD_LOG_FILE, DEPLOY_RESULT_FILE, STATE_FILE, envSegment, lambdaLogGroups, markerLines } from "./lib.mjs";

const DEFAULT_LOG_WINDOW_MS = 60 * 60 * 1000;

const MAX_EVENTS_PER_GROUP = 200;

const AWS_TIMEOUT_MS = 60_000;

const appDir = process.cwd();
const state = readJSON(join(appDir, STATE_FILE)) ?? {};
const result = readJSON(join(appDir, DEPLOY_RESULT_FILE)) ?? {};

for (const line of markerLines({ buildId: readBuildID(), deploymentId: readDeploymentID() })) {
  console.log(line);
}

replay(BUILD_LOG_FILE, join(appDir, BUILD_LOG_FILE));
replay("ocel.log", join(appDir, ".ocel", "logs", "ocel.log"));
printLambdaLogs();

function readBuildID() {
  const path = join(appDir, ".next", "BUILD_ID");
  if (existsSync(path)) {
    return readFileSync(path, "utf8").trim();
  }
  return result.apps?.[0]?.buildId;
}

function readDeploymentID() {
  return result.apps?.[0]?.deploymentId;
}

function replay(label, path) {
  console.log(`=== ${label} ===`);
  if (!existsSync(path)) {
    console.log(`(no ${label})`);
    return;
  }
  console.log(readFileSync(path, "utf8"));
}

function printLambdaLogs() {
  console.log("=== lambda logs ===");
  if (!state.slug) {
    console.log("(no deploy state; cannot resolve this app's functions)");
    return;
  }

  const env = result.environment ? envSegment(result.environment) : "";
  const filters = [`Key=ocel:project,Values=${state.slug}`, ...(env ? [`Key=ocel:env,Values=${env}`] : [])];

  let groups;
  try {
    groups = lambdaLogGroups(
      JSON.parse(
        aws([
          "resourcegroupstaggingapi",
          "get-resources",
          "--tag-filters",
          ...filters,
          "--resource-type-filters",
          "lambda:function",
        ]),
      ),
    );
  } catch (err) {
    console.log(`(could not resolve log groups: ${err.message})`);
    return;
  }

  if (groups.length === 0) {
    console.log(`(no functions tagged ${filters.join(" ")})`);
    return;
  }

  const startTime = Number(state.startedAt) || Date.now() - DEFAULT_LOG_WINDOW_MS;
  for (const group of groups) {
    console.log(`--- ${group} ---`);
    try {
      const events = JSON.parse(
        aws([
          "logs",
          "filter-log-events",
          "--log-group-name",
          group,
          "--start-time",
          String(startTime),
          "--limit",
          String(MAX_EVENTS_PER_GROUP),
        ]),
      );
      for (const event of events.events ?? []) {
        console.log(`${new Date(event.timestamp).toISOString()} ${(event.message ?? "").trimEnd()}`);
      }
    } catch (err) {
      console.log(`(could not read ${group}: ${err.message})`);
    }
  }
}

function aws(args) {
  return execFileSync("aws", [...args, "--output", "json"], {
    encoding: "utf8",
    timeout: AWS_TIMEOUT_MS,
    stdio: ["ignore", "pipe", "pipe"],
    maxBuffer: 32 * 1024 * 1024,
    env: { ...process.env, ...AWS_CLI_RETRY_ENV },
  });
}

function readJSON(path) {
  try {
    return JSON.parse(readFileSync(path, "utf8"));
  } catch {
    return null;
  }
}
