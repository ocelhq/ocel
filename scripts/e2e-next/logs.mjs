#!/usr/bin/env node
// NEXT_TEST_DEPLOY_LOGS_SCRIPT_PATH for the Next.js deployment-adapter
// compatibility harness. Runs with cwd set to the temp app, plus NEXT_TEST_DIR
// and NEXT_TEST_DEPLOY_URL.
//
// The three marker lines come first and everything else after, deliberately:
// the harness takes the FIRST match of each marker, and the replayed build log
// contains the fixture's own post-build markers (with an undefined deployment
// id) which must not win over ours.
//
// The harness treats a non-zero exit as a failed test run, and logs are a
// debugging aid, not a result — so every source here degrades to a note on
// stdout and this script always exits 0.

import { execFileSync } from "node:child_process";
import { existsSync, readFileSync } from "node:fs";
import { join } from "node:path";

import { BUILD_LOG_FILE, DEPLOY_RESULT_FILE, STATE_FILE, envSegment, lambdaLogGroups, markerLines } from "./lib.mjs";

// How far back CloudWatch is queried when the state file carries no start time.
const DEFAULT_LOG_WINDOW_MS = 60 * 60 * 1000;

// Per-log-group event cap, so one chatty Lambda cannot bury the rest.
const MAX_EVENTS_PER_GROUP = 200;

const AWS_TIMEOUT_MS = 60_000;

const appDir = process.cwd();
const state = readJSON(join(appDir, STATE_FILE)) ?? {};
const result = readJSON(join(appDir, DEPLOY_RESULT_FILE)) ?? {};

for (const line of markerLines({ buildId: readBuildID(), promotionId: result.promotionId })) {
  console.log(line);
}

replay(BUILD_LOG_FILE, join(appDir, BUILD_LOG_FILE));
replay("ocel.log", join(appDir, ".ocel", "logs", "ocel.log"));
printLambdaLogs();

// readBuildID prefers the build's own BUILD_ID file over the deploy result: it
// is written by the very build the harness is testing, whatever the CLI made of
// it afterwards.
function readBuildID() {
  const path = join(appDir, ".next", "BUILD_ID");
  if (existsSync(path)) {
    return readFileSync(path, "utf8").trim();
  }
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

// printLambdaLogs pulls this app's recent CloudWatch events. The functions are
// Pulumi-autonamed, so they are found by the ocel tags every Ocel function
// carries (cloud/aws/deploy/function.go). `ocel:env` is the one that isolates
// this app: a whole CI run shares one project, so `ocel:project` matches every
// fixture running concurrently, and `ocel:app` matches nothing narrower still
// (every temp app is declared under the constant APP_NAME). `ocel:env` is the
// asset key's environment segment — "preview-<this app's pointer>".
//
// It comes off the deploy result rather than the state file: the pointer is
// what the CLI resolved the ref to, not something these scripts derive. A
// deploy that produced no result falls back to the project filter, which mixes
// concurrent fixtures' logs together but is better than printing none.
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
  });
}

function readJSON(path) {
  try {
    return JSON.parse(readFileSync(path, "utf8"));
  } catch {
    return null;
  }
}
