#!/usr/bin/env node

import { join } from "path";
import { constants } from "node:os";
import { createRequire } from "node:module";

const { platform, arch } = process;
const require = createRequire(import.meta.url);

let packageName = "";

switch (platform) {
  case "win32":
    packageName = `win32-${arch}`;
    break;
  case "darwin":
    packageName = `darwin-${arch}`;
    break;
  case "linux":
    packageName = `linux-${arch}`;
    break;
  default:
    throw new Error(`Unsupported platform: ${platform}`);
}

const binaryPkg = `@ocel/${packageName}`;
const binary = platform === "win32" ? "ocel.exe" : "ocel";

let binaryPath = "";
try {
  binaryPath = require.resolve(join(binaryPkg, "bin", binary));
} catch {
  console.error(`Failed to locate binary for ${binaryPkg}.`);
  process.exit(1);
}

const { spawn } = require("child_process");
const child = spawn(binaryPath, process.argv.slice(2), { stdio: "inherit" });

process.on("SIGINT", () => {});
process.on("SIGTERM", () => child.kill("SIGTERM"));

child.on("error", (err) => {
  console.error(`Failed to run ${binaryPath}: ${err.message}`);
  process.exit(1);
});

child.on("exit", (code, signal) => {
  process.exit(signal ? 128 + (constants.signals[signal] ?? 0) : (code ?? 0));
});
