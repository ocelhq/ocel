import { spawnSync } from "node:child_process";
import { cpSync, mkdirSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { platformPackage } from "../bin/resolve.js";

const wrapperSource = join(dirname(fileURLToPath(import.meta.url)), "..", "bin");

let root = "";
let entry = "";

function install(script) {
  const target = join(root, "node_modules", platformPackage(process.platform, process.arch), "bin");
  mkdirSync(target, { recursive: true });
  writeFileSync(join(target, "ocel"), script, { mode: 0o755 });
}

beforeEach(() => {
  root = mkdtempSync(join(tmpdir(), "ocel-cli-"));
  entry = join(root, "wrapper", "ocel.js");
  cpSync(wrapperSource, join(root, "wrapper"), { recursive: true });
});

afterEach(() => {
  rmSync(root, { recursive: true, force: true });
});

describe.runIf(process.platform !== "win32")("the ocel wrapper", () => {
  it("hands its arguments to the platform binary", () => {
    install('#!/bin/sh\nprintf "%s\\n" "$@"\n');
    const run = spawnSync(process.execPath, [entry, "deploy", "--target", "prod"], {
      encoding: "utf8",
    });
    expect(run.stdout).toBe("deploy\n--target\nprod\n");
    expect(run.status).toBe(0);
  });

  it("exits with the status the platform binary exited with", () => {
    install("#!/bin/sh\nexit 3\n");
    const run = spawnSync(process.execPath, [entry], { encoding: "utf8" });
    expect(run.status).toBe(3);
  });

  it("reports the missing platform package rather than a stack trace", () => {
    const run = spawnSync(process.execPath, [entry], { encoding: "utf8" });
    expect(run.status).toBe(1);
    expect(run.stderr).toContain(platformPackage(process.platform, process.arch));
    expect(run.stderr).not.toContain("at ");
  });
});
