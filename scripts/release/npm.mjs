#!/usr/bin/env node

import { execFileSync, spawnSync } from "node:child_process";
import { copyFileSync, mkdtempSync, readdirSync, readFileSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { parse } from "./version.mjs";

const REPO_ROOT = resolve(dirname(fileURLToPath(import.meta.url)), "..", "..");

export const ORDER = [
  "cli-darwin-arm64",
  "cli-darwin-x64",
  "cli-linux-arm64",
  "cli-linux-x64",
  "cli-win32-x64",
  "cli",
  "ocel",
  "ocel-sst",
  "ocel-pulumi",
  "ocel-transforms",
];

export const LICENSING = ["LICENSE", "NOTICE"];

const DIST_TAGS = { stable: "latest", rc: "next", nightly: "nightly" };

export function distTag(version) {
  return DIST_TAGS[parse(version).channel];
}

export function published(name, version, run = spawnSync) {
  const result = run("npm", ["view", `${name}@${version}`, "version", "--json"], {
    cwd: tmpdir(),
    encoding: "utf8",
  });
  const answer = result.stdout ? JSON.parse(result.stdout) : undefined;
  if (result.status === 0) return answer === version;
  if (answer?.error?.code === "E404") return false;
  throw new Error(`npm view ${name}@${version} failed: ${result.stderr || result.error}`);
}

function main() {
  const args = process.argv.slice(2);
  const dryRun = args.includes("--dry-run");
  const [version] = args.filter((arg) => arg !== "--dry-run");
  if (!version) {
    console.error("usage: npm.mjs <version> [--dry-run]");
    process.exit(1);
  }
  const tag = distTag(version);
  const packed = mkdtempSync(join(tmpdir(), "ocel-npm-"));
  try {
    for (const dir of ORDER) {
      const cwd = join(REPO_ROOT, "packages", dir);
      const { name, version: stamped } = JSON.parse(
        readFileSync(join(cwd, "package.json"), "utf8"),
      );
      if (stamped !== version) {
        throw new Error(`${name} is stamped ${stamped}, not ${version}`);
      }
      if (published(name, version)) {
        console.error(`${name}@${version} is already on npm`);
        continue;
      }
      for (const file of LICENSING) copyFileSync(join(REPO_ROOT, file), join(cwd, file));
      const destination = mkdtempSync(join(packed, "pack-"));
      execFileSync("pnpm", ["pack", "--pack-destination", destination], {
        cwd,
        stdio: ["ignore", 2, "inherit"],
      });
      const [tarball] = readdirSync(destination);
      const publish = ["publish", join(destination, tarball), "--access", "public", "--tag", tag];
      publish.push(dryRun ? "--dry-run" : "--provenance");
      execFileSync("npm", publish, { cwd: destination, stdio: ["ignore", 2, "inherit"] });
    }
  } finally {
    rmSync(packed, { recursive: true, force: true });
  }
}

if (import.meta.main) main();
