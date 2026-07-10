#!/usr/bin/env node
// Shared cross-compile script for the Ocel Go binaries.
//
// This is the single implementation of the Node platform/arch <-> Go
// GOOS/GOARCH mapping and the `go build` invocation itself, reused by both
// local dev (`.air.toml` via `--host`) and CI (`release.yml`'s
// build-binaries matrix via explicit `--goos`/`--goarch`).
//
// Two targets share the mapping:
//   --target cli       (default) the `ocel` CLI -> native-lib/cli-<node>/bin/ocel
//   --target provider  the AWS provider distribution -> native-lib/
//                       provider-aws-<node>/bin/{ocelaws, runtime/ocelawsrt}
// The provider target builds BOTH binaries the provider package ships: the
// provider itself (cmd/ocelaws) and the runtime (cmd/ocelawsrt). The "ocel"
// prefix keeps the shipped provider binary from shadowing the host's real
// `aws` CLI.
//
// Usage:
//   node scripts/build-native.mjs --host [--target <cli|provider>] [--version <version>]
//   node scripts/build-native.mjs --goos <goos> --goarch <goarch> [--target <cli|provider>] [--out <path>] [--version <version>]

import { spawnSync } from "node:child_process";
import { chmodSync, existsSync, mkdirSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const __dirname = dirname(fileURLToPath(import.meta.url));
const REPO_ROOT = resolve(__dirname, "..");

// exeName appends the Windows extension when building for windows.
function exeName(name, goos) {
  return goos === "windows" ? `${name}.exe` : name;
}

// TARGETS declares, per buildable distribution, its Go module directory, the
// npm native-package family its binaries land in, the -X ldflags package for
// its version string, and the binaries it ships (each an entrypoint dir under
// cmd/ plus the output name and optional subdir inside the package's bin/).
const TARGETS = {
  cli: {
    goModuleDir: join(REPO_ROOT, "cli"),
    pkgPrefix: "cli",
    versionLdflagPkg: "github.com/ocelhq/ocel/cli/internal/cli",
    binaries: [{ cmd: "./ocel", name: "ocel" }],
  },
  provider: {
    goModuleDir: join(REPO_ROOT, "cloud", "aws"),
    pkgPrefix: "provider-aws",
    // Both cmd/ocelaws and cmd/ocelawsrt are `package main`, so their version
    // string is `-X main.version`.
    versionLdflagPkg: "main",
    binaries: [
      { cmd: "./cmd/ocelaws", name: "ocelaws" },
      { cmd: "./cmd/ocelawsrt", name: "ocelawsrt", subdir: "runtime" },
    ],
  },
};

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
  const args = { host: false, target: "cli" };
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
      case "--target":
        args.target = argv[++i];
        break;
      case "--version":
        args.version = argv[++i];
        break;
      default:
        throw new Error(`Unknown argument: ${arg}`);
    }
  }
  if (!TARGETS[args.target]) {
    throw new Error(`Unknown --target: ${args.target} (expected cli or provider)`);
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

// binaryOutPath is where one of a build target's binaries lands inside its
// native package's bin/ for a given host platform.
function binaryOutPath(buildTarget, binary, platform) {
  const pkgDir = `${buildTarget.pkgPrefix}-${platform.nodePlatform}-${platform.nodeArch}`;
  const name = exeName(binary.name, platform.goos);
  const parts = [REPO_ROOT, "packages", "native-lib", pkgDir, "bin"];
  if (binary.subdir) parts.push(binary.subdir);
  parts.push(name);
  return join(...parts);
}

function buildOne(buildTarget, binary, platform, outPath, version) {
  mkdirSync(dirname(outPath), { recursive: true });

  const buildArgs = ["build", "-o", outPath];
  if (version) {
    buildArgs.push("-ldflags", `-X ${buildTarget.versionLdflagPkg}.version=${version}`);
  }
  buildArgs.push(binary.cmd);

  const result = spawnSync("go", buildArgs, {
    cwd: buildTarget.goModuleDir,
    stdio: "inherit",
    env: {
      ...process.env,
      // Cross-compilation from a single Linux CI runner requires this:
      // go-keyring's per-OS backends all shell out to native tools rather
      // than linking natively via cgo, so CGO is never actually needed.
      CGO_ENABLED: "0",
      GOOS: platform.goos,
      GOARCH: platform.goarch,
    },
  });

  if (result.status !== 0) {
    process.exit(result.status ?? 1);
  }

  if (platform.goos !== "windows" && existsSync(outPath)) {
    chmodSync(outPath, 0o755);
  }

  console.log(`Built ${platform.goos}/${platform.goarch} -> ${outPath}`);
}

function main() {
  const args = parseArgs(process.argv.slice(2));
  const platform = resolveTarget(args);
  const buildTarget = TARGETS[args.target];

  // --out overrides the destination, but only for a single-binary target (the
  // CLI); a multi-binary target owns its layout.
  if (args.out && buildTarget.binaries.length > 1) {
    throw new Error(`--out is not supported for --target ${args.target} (it ships multiple binaries)`);
  }

  for (const binary of buildTarget.binaries) {
    const outPath = args.out
      ? resolve(args.out)
      : binaryOutPath(buildTarget, binary, platform);
    buildOne(buildTarget, binary, platform, outPath, args.version);
  }
}

main();
