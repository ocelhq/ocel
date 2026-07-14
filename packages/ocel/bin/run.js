#!/usr/bin/env node

import { dirname, join } from "path";
import { fileURLToPath } from "node:url";
import { createRequire } from "node:module";

const { platform, arch } = process;
const require = createRequire(import.meta.url);

// The ocel package root: this file lives at <root>/bin/run.js, so root is the
// parent of bin/. The Go binary reads OCEL_BUILDER_PATH to locate the node
// builder entry directly, without any path stitching of its own.
const packageRoot = dirname(dirname(fileURLToPath(import.meta.url)));
const builderPath = join(packageRoot, "dist", "builder", "cli.js");

const nextAdapterPath = require.resolve("@ocel/next-runtime");
const nextWorkerPath = require.resolve("@ocel/worker-nextjs")

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
  // OCEL_HOME (the package root) is no longer read by the Go CLI, which now
  // locates the builder via OCEL_BUILDER_PATH; it is kept exported for future
  // tooling that may need to resolve other package-relative assets.
  const result = spawnSync(binaryPath, process.argv.slice(2), {
    stdio: "inherit",
    env: {
      ...process.env,
      OCEL_HOME: packageRoot,
      OCEL_BUILDER_PATH: builderPath,
      NEXT_ADAPTER_PATH: nextAdapterPath,
      OCEL_NEXT_WORKER_PATH: nextWorkerPath
    },
  });
  process.exit(result.status);
} catch (e) {
  console.error(`Failed to locate binary for ${binaryPkg}.`);
  process.exit(1);
}
