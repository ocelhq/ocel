import { spawnSync } from "node:child_process";
import { chmodSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterEach, beforeEach, describe, expect, it } from "vitest";

import {
  COMPAT_TARGET_ENV,
  DEPLOY_REPORT_FILE,
  GCP_PROJECT_ENV,
  GCP_REGION_ENV,
  NAMESPACE_ENV,
  NEXT_COMPAT_NAMESPACE,
  STATE_FILE,
} from "./lib.mjs";

const STARTED_AT = Date.UTC(2026, 0, 1, 12, 0, 0);

const FAKE_AWS = `#!/usr/bin/env node
const { appendFileSync, readFileSync } = require("node:fs");
const args = process.argv.slice(2);
appendFileSync(process.env.FAKE_AWS_CALLS, JSON.stringify(args) + "\\n");
if (process.env.FAKE_AWS_FAILS) {
  process.stderr.write("An error occurred (AccessDenied)\\n");
  process.exit(1);
}
if (args.includes("get-resources")) {
  console.log(JSON.stringify({ ResourceTagMappingList: [
    { ResourceARN: "arn:aws:lambda:us-east-1:111111111111:function:app-server" },
  ] }));
} else if (args.includes("get-function-configuration")) {
  console.log("/aws/lambda/app-server");
} else if (args.includes("filter-log-events")) {
  console.log(readFileSync(process.env.FAKE_AWS_EVENTS, "utf8"));
}
`;

const FAKE_OCEL = `console.log("TEARDOWN " + process.argv.slice(2).join(" "));\n`;

let root;
let appDir;
let adapterDir;
let binDir;

beforeEach(() => {
  root = mkdtempSync(join(tmpdir(), "ocel-cleanup-"));
  appDir = join(root, "app");
  adapterDir = join(root, "adapter");
  binDir = join(root, "bin");
  mkdirSync(join(appDir, ".ocel"), { recursive: true });
  mkdirSync(join(adapterDir, "packages", "cli", "bin"), { recursive: true });
  mkdirSync(binDir);
  writeFileSync(join(adapterDir, "packages", "cli", "bin", "ocel.js"), FAKE_OCEL);
  writeFileSync(join(binDir, "aws"), FAKE_AWS);
  chmodSync(join(binDir, "aws"), 0o755);
  writeFileSync(join(appDir, "ocel.config.ts"), "export default {};\n");
  writeFileSync(
    join(appDir, STATE_FILE),
    JSON.stringify({ slug: "e2e-run", name: "pr-app", startedAt: STARTED_AT }),
  );
  writeFileSync(
    join(appDir, DEPLOY_REPORT_FILE),
    JSON.stringify({ apps: [{ storagePrefix: "preview-pr-app/e2e-run/app/r1/" }] }),
  );
  writeEvents(1);
});

afterEach(() => {
  rmSync(root, { recursive: true, force: true });
});

function writeEvents(count) {
  const events = Array.from({ length: count }, (_, i) => ({
    timestamp: STARTED_AT + i * 1000,
    message: `origin line ${i}`,
  }));
  writeFileSync(join(root, "events.json"), JSON.stringify({ events }));
}

function runCleanup(env = {}) {
  const ran = spawnSync(process.execPath, [join(import.meta.dirname, "cleanup.mjs")], {
    cwd: appDir,
    env: {
      PATH: `${binDir}:${process.env.PATH}`,
      HOME: process.env.HOME,
      [NAMESPACE_ENV]: NEXT_COMPAT_NAMESPACE,
      ADAPTER_DIR: adapterDir,
      FAKE_AWS_CALLS: join(root, "calls.ndjson"),
      FAKE_AWS_EVENTS: join(root, "events.json"),
      ...env,
    },
    encoding: "utf8",
    timeout: 60_000,
  });
  return { ...ran, lines: ran.stdout.split("\n") };
}

function awsCalls() {
  try {
    return readFileSync(join(root, "calls.ndjson"), "utf8")
      .trim()
      .split("\n")
      .filter(Boolean)
      .map((line) => JSON.parse(line));
  } catch {
    return [];
  }
}

describe("cleanup", () => {
  it("prints the app's function logs from deploy to teardown before it removes the preview", () => {
    const { status, lines } = runCleanup();

    expect(status).toBe(0);
    const logged = lines.indexOf("2026-01-01T12:00:00.000Z origin line 0");
    const removed = lines.findIndex((line) => line.startsWith("TEARDOWN preview rm pr-app"));
    expect(logged).toBeGreaterThan(-1);
    expect(removed).toBeGreaterThan(logged);

    const read = awsCalls().find((args) => args.includes("filter-log-events"));
    expect(read).toContain("/aws/lambda/app-server");
    expect(read[read.indexOf("--start-time") + 1]).toBe(String(STARTED_AT));
    expect(Number(read[read.indexOf("--end-time") + 1])).toBeGreaterThan(STARTED_AT);
  });

  it("prints only the newest lines of a busy window", () => {
    writeEvents(1000);

    const { status, lines } = runCleanup();

    expect(status).toBe(0);
    expect(lines).toContain("(700 earlier events left out)");
    expect(lines).toContain("2026-01-01T12:16:39.000Z origin line 999");
    expect(lines).not.toContain("2026-01-01T12:00:00.000Z origin line 0");
  });

  it("removes the preview when the logs cannot be read", () => {
    const { status, lines } = runCleanup({ FAKE_AWS_FAILS: "1" });

    expect(status).toBe(0);
    expect(lines.some((line) => line.startsWith("(could not resolve this app's functions"))).toBe(
      true,
    );
    expect(lines.some((line) => line.startsWith("TEARDOWN preview rm pr-app"))).toBe(true);
  });

  it("reads no AWS logs on a gcp target", () => {
    const { lines } = runCleanup({
      [COMPAT_TARGET_ENV]: "gcp-alb",
      [GCP_PROJECT_ENV]: "proj",
      [GCP_REGION_ENV]: "us-central1",
    });

    expect(awsCalls()).toEqual([]);
    expect(lines.some((line) => line.startsWith("TEARDOWN preview rm pr-app"))).toBe(true);
  });
});
