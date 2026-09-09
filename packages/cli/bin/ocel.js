#!/usr/bin/env node

import { spawn } from "node:child_process";
import { createRequire } from "node:module";
import { constants } from "node:os";
import { binaryPath } from "./resolve.js";

const require = createRequire(import.meta.url);

let binary = "";
try {
  binary = binaryPath({
    platform: process.platform,
    arch: process.arch,
    resolve: (specifier) => require.resolve(specifier),
  });
} catch (error) {
  console.error(error.message);
  process.exit(1);
}

const child = spawn(binary, process.argv.slice(2), { stdio: "inherit" });

process.on("SIGINT", () => {});
process.on("SIGTERM", () => child.kill("SIGTERM"));

child.on("error", (error) => {
  console.error(`could not run ${binary}: ${error.message}`);
  process.exit(1);
});

child.on("exit", (code, signal) => {
  process.exit(signal ? 128 + (constants.signals[signal] ?? 0) : (code ?? 0));
});
