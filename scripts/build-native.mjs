#!/usr/bin/env node
// Shared cross-compile script for the `ocel` Go CLI.
//
// This is the single implementation of the Node platform/arch <-> Go
// GOOS/GOARCH mapping and the `go build` invocation itself, reused by both
// local dev (`ocel/.air.toml` via `--host`) and CI (`release.yml`'s
// build-binaries matrix via explicit `--goos`/`--goarch`).
//
// Usage:
//   node scripts/build-native.mjs --host [--version <version>]
//   node scripts/build-native.mjs --goos <goos> --goarch <goarch> --out <path> [--version <version>]

import { spawnSync } from "node:child_process";
import { chmodSync, existsSync, mkdirSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const __dirname = dirname(fileURLToPath(import.meta.url));
const REPO_ROOT = resolve(__dirname, "..");
const GO_MODULE_DIR = join(REPO_ROOT, "ocel");
const VERSION_LDFLAG_PKG = "github.com/ocelhq/ocel/internal/cli";

// Single source of truth for the platform matrix already encoded in
// `packages/ocel/package.json`'s `optionalDependencies`. Node's
// `process.platform`/`process.arch` values on one side, Go's
// `GOOS`/`GOARCH` values on the other.
const PLATFORM_MATRIX = [
  { nodePlatform: "darwin", nodeArch: "arm64", goos: "darwin", goarch: "arm64" },
  { nodePlatform: "darwin", nodeArch: "x64", goos: "darwin", goarch: "amd64" },
  { nodePlatform: "linux", nodeArch: "x64", goos: "linux", goarch: "amd64" },
  { nodePlatform: "win32", nodeArch: "x64", goos: "windows", goarch: "amd64" },
];

function findByGo(goos, goarch) {
  return PLATFORM_MATRIX.find((entry) => entry.goos === goos && entry.goarch === goarch);
}

function findByNode(nodePlatform, nodeArch) {
  return PLATFORM_MATRIX.find(
    (entry) => entry.nodePlatform === nodePlatform && entry.nodeArch === nodeArch,
  );
}

function parseArgs(argv) {
  const args = { host: false };
  for (let i = 0; i < argv.length; i++) {
    const arg = argv[i];
    switch (arg) {
      case "--host":
        args.host = true;
        break;
      case "--goos":
        args.goos = argv[++i];
        break;
      case "--goarch":
        args.goarch = argv[++i];
        break;
      case "--out":
        args.out = argv[++i];
        break;
      case "--version":
        args.version = argv[++i];
        break;
      default:
        throw new Error(`Unknown argument: ${arg}`);
    }
  }
  return args;
}

function resolveTarget(args) {
  if (args.host) {
    const entry = findByNode(process.platform, process.arch);
    if (!entry) {
      throw new Error(`Unsupported host platform: ${process.platform}-${process.arch}`);
    }
    return entry;
  }

  if (!args.goos || !args.goarch) {
    throw new Error("Either --host, or both --goos and --goarch, must be provided.");
  }

  const entry = findByGo(args.goos, args.goarch);
  if (!entry) {
    throw new Error(`Unsupported GOOS/GOARCH combination: ${args.goos}/${args.goarch}`);
  }
  return entry;
}

function defaultOutPath(entry) {
  const binaryName = entry.goos === "windows" ? "ocel.exe" : "ocel";
  const pkgDir = `cli-${entry.nodePlatform}-${entry.nodeArch}`;
  return join(REPO_ROOT, "packages", "native-lib", pkgDir, "bin", binaryName);
}

function main() {
  const args = parseArgs(process.argv.slice(2));
  const target = resolveTarget(args);

  const outPath = args.out ? resolve(args.out) : defaultOutPath(target);
  mkdirSync(dirname(outPath), { recursive: true });

  const buildArgs = ["build", "-o", outPath];
  if (args.version) {
    buildArgs.push("-ldflags", `-X ${VERSION_LDFLAG_PKG}.version=${args.version}`);
  }
  buildArgs.push("./cmd/ocel");

  const result = spawnSync("go", buildArgs, {
    cwd: GO_MODULE_DIR,
    stdio: "inherit",
    env: {
      ...process.env,
      // Cross-compilation from a single Linux CI runner requires this:
      // go-keyring's per-OS backends all shell out to native tools rather
      // than linking natively via cgo, so CGO is never actually needed.
      CGO_ENABLED: "0",
      GOOS: target.goos,
      GOARCH: target.goarch,
    },
  });

  if (result.status !== 0) {
    process.exit(result.status ?? 1);
  }

  if (target.goos !== "windows" && existsSync(outPath)) {
    chmodSync(outPath, 0o755);
  }

  console.log(`Built ${target.goos}/${target.goarch} -> ${outPath}`);
}

main();
