#!/usr/bin/env node

import { spawnSync } from "node:child_process";
import { chmodSync, existsSync, mkdirSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const __dirname = dirname(fileURLToPath(import.meta.url));
const REPO_ROOT = resolve(__dirname, "..");

function exeName(name, goos) {
  return goos === "windows" ? `${name}.exe` : name;
}

const TARGETS = {
  cli: {
    goModuleDir: join(REPO_ROOT, "cli"),
    pkgPrefix: "cli",
    versionLdflagPkg: "github.com/ocelhq/ocel/cli/internal/cli",
    generate: true,
    binaries: [{ cmd: "./ocel", name: "ocel" }],
  },
  provider: {
    goModuleDir: join(REPO_ROOT, "platform", "aws", "provider"),
    pkgPrefix: "provider-aws",
    versionLdflagPkg: "main",
    binaries: [
      { cmd: "./cmd/deploy", name: "deploy" },
      { cmd: "./cmd/runtime", name: "runtime" },
    ],
  },
};

const PLATFORM_MATRIX = [
  {
    nodePlatform: "darwin",
    nodeArch: "arm64",
    goos: "darwin",
    goarch: "arm64",
  },
  { nodePlatform: "darwin", nodeArch: "x64", goos: "darwin", goarch: "amd64" },
  { nodePlatform: "linux", nodeArch: "x64", goos: "linux", goarch: "amd64" },
  { nodePlatform: "win32", nodeArch: "x64", goos: "windows", goarch: "amd64" },
];

function findByGo(goos, goarch) {
  return PLATFORM_MATRIX.find(
    (entry) => entry.goos === goos && entry.goarch === goarch,
  );
}

function findByNode(nodePlatform, nodeArch) {
  return PLATFORM_MATRIX.find(
    (entry) =>
      entry.nodePlatform === nodePlatform && entry.nodeArch === nodeArch,
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
    throw new Error(
      `Unknown --target: ${args.target} (expected cli or provider)`,
    );
  }
  return args;
}

function resolveTarget(args) {
  if (args.host) {
    const entry = findByNode(process.platform, process.arch);
    if (!entry) {
      throw new Error(
        `Unsupported host platform: ${process.platform}-${process.arch}`,
      );
    }
    return entry;
  }

  if (!args.goos || !args.goarch) {
    throw new Error(
      "Either --host, or both --goos and --goarch, must be provided.",
    );
  }

  const entry = findByGo(args.goos, args.goarch);
  if (!entry) {
    throw new Error(
      `Unsupported GOOS/GOARCH combination: ${args.goos}/${args.goarch}`,
    );
  }
  return entry;
}

function binaryOutPath(buildTarget, binary, platform) {
  const pkgDir = `${buildTarget.pkgPrefix}-${platform.nodePlatform}-${platform.nodeArch}`;
  const name = exeName(binary.name, platform.goos);
  const parts = [REPO_ROOT, "packages", "native-lib", pkgDir, "bin"];
  if (binary.subdir) parts.push(binary.subdir);
  parts.push(name);
  return join(...parts);
}

function generate(buildTarget) {
  const result = spawnSync("go", ["generate", "./..."], {
    cwd: buildTarget.goModuleDir,
    stdio: "inherit",
  });
  if (result.status !== 0) {
    process.exit(result.status ?? 1);
  }
}

function buildOne(buildTarget, binary, platform, outPath, version) {
  mkdirSync(dirname(outPath), { recursive: true });

  const buildArgs = ["build", "-o", outPath];
  if (version) {
    buildArgs.push(
      "-ldflags",
      `-X ${buildTarget.versionLdflagPkg}.version=${version}`,
    );
  }
  buildArgs.push(binary.cmd);

  const result = spawnSync("go", buildArgs, {
    cwd: buildTarget.goModuleDir,
    stdio: "inherit",
    env: {
      ...process.env,
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

  if (args.out && buildTarget.binaries.length > 1) {
    throw new Error(
      `--out is not supported for --target ${args.target} (it ships multiple binaries)`,
    );
  }

  if (buildTarget.generate) {
    generate(buildTarget);
  }

  for (const binary of buildTarget.binaries) {
    const outPath = args.out
      ? resolve(args.out)
      : binaryOutPath(buildTarget, binary, platform);
    buildOne(buildTarget, binary, platform, outPath, args.version);
  }
}

main();
