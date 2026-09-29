#!/usr/bin/env node

import { execFileSync, spawnSync } from "node:child_process";
import { existsSync, mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join, relative, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { selectGoModules, selectSetup, touches } from "./selection.mjs";

const REPO_ROOT = resolve(dirname(fileURLToPath(import.meta.url)), "..", "..");
const UPSTREAM = "origin/main";

const SETUP = new Map([
  [
    "cli",
    [
      ["pnpm", "turbo", "run", "build", "--filter=ocel"],
      ["go", "generate", "-C", "cli", "./..."],
    ],
  ],
  ["platform/aws/provider", [["go", "generate", "-C", "platform/aws/provider", "./..."]]],
  ["platform/gcp/provider", [["go", "generate", "-C", "platform/gcp/provider", "./..."]]],
  ["platform/vps/provider", [["go", "generate", "-C", "platform/vps/provider", "./..."]]],
  ["pkg/provider/transform", [["go", "generate", "-C", "pkg/provider/transform", "./..."]]],
  [
    "platform/edge/cloudflare/deploy",
    [["go", "generate", "-C", "platform/edge/cloudflare/deploy", "./..."]],
  ],
]);

function read(command, args) {
  return execFileSync(command, args, { cwd: REPO_ROOT, encoding: "utf8" }).trim();
}

function lines(text) {
  return text.split("\n").filter(Boolean);
}

function changedFiles(base) {
  const tracked = lines(read("git", ["diff", "--name-only", base]));
  const untracked = lines(read("git", ["ls-files", "--others", "--exclude-standard"]));
  return [...new Set([...tracked, ...untracked])].sort();
}

function goModules() {
  return lines(read("go", ["list", "-m", "-f", "{{.Dir}}\t{{.Path}}\t{{.GoMod}}"])).map((line) => {
    const [dir, path, goMod] = line.split("\t");
    const { Require = [] } = JSON.parse(read("go", ["mod", "edit", "-json", goMod]));
    return {
      dir: relative(REPO_ROOT, dir),
      path,
      requires: Require.map((required) => required.Path),
    };
  });
}

const failed = [];

function run(argv, dir = ".") {
  const where = dir === "." ? "" : `   (in ${dir})`;
  console.log(`\n$ ${argv.join(" ")}${where}`);
  const { status, error } = spawnSync(argv[0], argv.slice(1), {
    cwd: join(REPO_ROOT, dir),
    stdio: "inherit",
  });
  if (error || status !== 0) failed.push(`${argv.join(" ")}${where}`);
}

function checkGo(changed) {
  const modules = goModules();
  const selected = selectGoModules(changed, modules);
  if (selected.length === 0) {
    console.log("go: no module changed");
    return;
  }
  for (const { dir, reason } of selected) console.log(`go: ${dir} (${reason})`);

  const dirs = selected.map((module) => module.dir);
  for (const dir of selectSetup(dirs, modules, [...SETUP.keys()])) {
    for (const argv of SETUP.get(dir)) run(argv);
  }

  const vettool = mkdtempSync(join(tmpdir(), "ocel-check-"));
  try {
    run(["go", "build", "-o", join(vettool, "redactvet"), "./scripts/redactvet"]);
    for (const dir of dirs) {
      run(["go", "mod", "tidy", "-diff"], dir);
      run(["golangci-lint", "run", "--allow-serial-runners", "./..."], dir);
      run(["go", "vet", `-vettool=${join(vettool, "redactvet")}`, "./..."], dir);
      run(["go", "test", "-race", "./..."], dir);
    }
  } finally {
    rmSync(vettool, { recursive: true, force: true });
  }
}

function checkJS(changed, base) {
  const present = changed.filter((file) => existsSync(join(REPO_ROOT, file)));
  if (present.length > 0) {
    run([
      "pnpm",
      "exec",
      "biome",
      "ci",
      "--no-errors-on-unmatched",
      "--files-ignore-unknown=true",
      ...present,
    ]);
  }
  run(["pnpm", "turbo", "run", "test", `--filter=...[${base}]`]);
}

function checkPython(changed) {
  if (!touches(changed, "python")) return;
  console.log("python: changed");
  run(["uv", "sync", "--all-extras"], "python");
  run(["uv", "run", "pytest", "-q"], "python");
}

function checkCrates(changed) {
  if (!touches(changed, "crates")) return;
  console.log("crates: changed");
  run(["cargo", "test", "--all-features"], "crates");
}

const base = read("git", ["merge-base", "HEAD", UPSTREAM]);
const changed = changedFiles(base);
console.log(
  `check: ${changed.length} files changed since ${base.slice(0, 12)} (merge-base with ${UPSTREAM})`,
);
if (changed.length === 0) process.exit(0);

checkGo(changed);
checkJS(changed, base);
checkPython(changed);
checkCrates(changed);

if (failed.length > 0) {
  console.error(`\ncheck: ${failed.length} failed:`);
  for (const step of failed) console.error(`  ${step}`);
  process.exit(1);
}
console.log("\ncheck: passed");
