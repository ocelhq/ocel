#!/usr/bin/env node

import { spawnSync } from "node:child_process";
import { copyFileSync, mkdirSync, readFileSync, rmSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const REPO_ROOT = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const DIST = join(REPO_ROOT, "dist");
const PROVIDERS = join(DIST, "providers");

function build(argv) {
  const args = ["build", "--snapshot", "--clean"];
  if (!argv.includes("--all")) args.push("--single-target");
  const result = spawnSync("goreleaser", args, {
    cwd: REPO_ROOT,
    stdio: ["inherit", 2, "inherit"],
  });
  if (result.error) {
    throw new Error(
      `goreleaser is not on PATH: ${result.error.message}. Run \`mise install\` to get the pinned version.`,
    );
  }
  if (result.status !== 0) process.exit(result.status ?? 1);
}

function read(name) {
  return JSON.parse(readFileSync(join(DIST, name), "utf8"));
}

function layout() {
  const { version } = read("metadata.json");
  const binaries = read("artifacts.json").filter((a) => a.type === "Binary");

  rmSync(PROVIDERS, { recursive: true, force: true });

  let cli;
  for (const binary of binaries) {
    if (binary.extra.ID === "ocel") {
      cli = join(REPO_ROOT, binary.path);
      continue;
    }
    const name = binary.extra.ID.replace(/^provider-/, "");
    const dir = join(PROVIDERS, name, version, `${binary.goos}-${binary.goarch}`);
    mkdirSync(dir, { recursive: true });
    copyFileSync(join(REPO_ROOT, binary.path), join(dir, binary.name));
  }
  return { cli, version };
}

function main() {
  build(process.argv.slice(2));
  const { cli, version } = layout();
  console.log(`OCEL_VERSION=${version}`);
  console.log(`OCEL_BIN=${cli}`);
  console.log(`OCEL_PROVIDERS_DIR=${PROVIDERS}`);
}

main();
