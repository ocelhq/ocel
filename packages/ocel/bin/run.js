#!/usr/bin/env node

import { dirname, join } from "path";
import { fileURLToPath } from "node:url";
import { createRequire } from "node:module";

const { platform, arch } = process;
const require = createRequire(import.meta.url);

// The ocel package root: this file lives at <root>/bin/run.js, so root is the
// parent of bin/. The Go binary reads OCEL_HOME to resolve the node builder at
// ${OCEL_HOME}/dist/builder/cli.js.
const packageRoot = dirname(dirname(fileURLToPath(import.meta.url)));

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

try {
  const binary = process.platform === "win32" ? "ocel.exe" : "ocel";
  const binaryPath = require.resolve(join(binaryPkg, "bin", binary));

  const { spawnSync } = require("child_process");
  const result = spawnSync(binaryPath, process.argv.slice(2), {
    stdio: "inherit",
    env: { ...process.env, OCEL_HOME: packageRoot },
  });
  process.exit(result.status);
} catch (e) {
  console.error(`Failed to locate binary for ${binaryPkg}.`);
  process.exit(1);
}
