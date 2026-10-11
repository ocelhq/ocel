import { spawn, spawnSync } from "node:child_process";
import { cpSync, mkdirSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
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

  it("tells the platform binary it was installed through npm", () => {
    install('#!/bin/sh\nprintf "%s" "$OCEL_INSTALL_METHOD"\n');
    const run = spawnSync(process.execPath, [entry], {
      encoding: "utf8",
      env: { ...process.env, OCEL_INSTALL_METHOD: "" },
    });
    expect(run.stdout).toBe("npm");
  });

  it("exits with the status the platform binary exited with", () => {
    install("#!/bin/sh\nexit 3\n");
    const run = spawnSync(process.execPath, [entry], { encoding: "utf8" });
    expect(run.status).toBe(3);
  });

  it.each(["SIGINT", "SIGTERM"])(
    "stops the platform binary when the wrapper alone is sent %s",
    async (signal) => {
      install(
        '#!/bin/sh\ntrap \'echo "stopped by TERM"; exit 0\' TERM\necho "$$"\nwhile :; do sleep 0.05; done\n',
      );
      const wrapper = spawn(process.execPath, [entry], { stdio: ["ignore", "pipe", "inherit"] });
      let output = "";
      wrapper.stdout.setEncoding("utf8");
      wrapper.stdout.on("data", (chunk) => {
        output += chunk;
      });
      const exited = new Promise((resolve) => wrapper.on("close", resolve));
      await vi.waitFor(() => expect(output).toMatch(/^\d+\n/), { timeout: 5000 });
      const binary = Number(output.split("\n")[0]);

      try {
        wrapper.kill(signal);
        const outcome = await Promise.race([
          exited.then(() => "exited"),
          new Promise((resolve) => setTimeout(() => resolve("running"), 5000)),
        ]);
        expect(outcome).toBe("exited");
        expect(output).toContain("stopped by TERM");
        expect(() => process.kill(binary, 0)).toThrow();
      } finally {
        wrapper.kill("SIGKILL");
        try {
          process.kill(binary, "SIGKILL");
        } catch {}
      }
    },
    10000,
  );

  it("stops the platform binary when SIGTERM reaches the wrapper the moment it spawned it", async () => {
    install("#!/bin/sh\nwhile :; do sleep 0.05; done\n");
    const preload = join(root, "signal-on-spawn.mjs");
    writeFileSync(
      preload,
      [
        'import childProcess from "node:child_process";',
        'import { syncBuiltinESMExports } from "node:module";',
        "const spawn = childProcess.spawn;",
        "childProcess.spawn = (...args) => {",
        "  const child = spawn(...args);",
        '  process.kill(process.pid, "SIGTERM");',
        "  return child;",
        "};",
        "syncBuiltinESMExports();",
      ].join("\n"),
    );
    const wrapper = spawn(process.execPath, ["--import", preload, entry], {
      stdio: ["ignore", "pipe", "inherit"],
    });
    wrapper.stdout.resume();
    const exited = new Promise((resolve) => wrapper.on("close", resolve));

    try {
      const outcome = await Promise.race([
        exited.then(() => "exited"),
        new Promise((resolve) => setTimeout(() => resolve("running"), 5000)),
      ]);
      expect(outcome).toBe("exited");
      expect(wrapper.signalCode).toBeNull();
    } finally {
      wrapper.kill("SIGKILL");
      spawnSync("pkill", ["-KILL", "-f", join(root, "node_modules")]);
    }
  }, 10000);

  it("reports the missing platform package rather than a stack trace", () => {
    const run = spawnSync(process.execPath, [entry], { encoding: "utf8" });
    expect(run.status).toBe(1);
    expect(run.stderr).toContain(platformPackage(process.platform, process.arch));
    expect(run.stderr).not.toContain("at ");
  });
});
