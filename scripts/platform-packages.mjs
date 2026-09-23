import { execFileSync } from "node:child_process";
import { createHash } from "node:crypto";
import { chmodSync, mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { join } from "node:path";

const NOTICES = "THIRD_PARTY_NOTICES";

const [archives, version, packages] = process.argv.slice(2);
if (!archives || !version || !packages) {
  console.error(
    "usage: platform-packages.mjs <release archives directory> <version> <packages directory>",
  );
  process.exit(1);
}

const targets = [
  ["darwin", "arm64", "cli-darwin-arm64"],
  ["darwin", "amd64", "cli-darwin-x64"],
  ["linux", "arm64", "cli-linux-arm64"],
  ["linux", "amd64", "cli-linux-x64"],
  ["windows", "amd64", "cli-win32-x64"],
];

const sums = new Map(
  readFileSync(join(archives, "checksums.txt"), "utf8")
    .split("\n")
    .map((line) => line.trim().split(/\s+/))
    .filter((fields) => fields.length === 2)
    .map(([sum, name]) => [name, sum]),
);

const failures = [];
for (const [goos, goarch, target] of targets) {
  const windows = goos === "windows";
  const archive = `ocel_${version}_${goos}_${goarch}.${windows ? "zip" : "tar.gz"}`;
  const binary = windows ? "ocel.exe" : "ocel";
  const expected = sums.get(archive);
  if (!expected) {
    failures.push(`checksums.txt names no ${archive} for ${target}`);
    continue;
  }
  let bytes;
  try {
    bytes = readFileSync(join(archives, archive));
  } catch {
    failures.push(`the release has no ${archive} for ${target}`);
    continue;
  }
  const actual = createHash("sha256").update(bytes).digest("hex");
  if (actual !== expected) {
    failures.push(`${archive} hashes to ${actual}, not the ${expected} checksums.txt names`);
    continue;
  }
  const path = join(archives, archive);
  const extract = (member) =>
    windows
      ? execFileSync("unzip", ["-p", path, member], { maxBuffer: 1 << 30 })
      : execFileSync("tar", ["-xzOf", path, member], { maxBuffer: 1 << 30 });
  const bin = join(packages, target, "bin");
  mkdirSync(bin, { recursive: true });
  const placed = join(bin, binary);
  writeFileSync(placed, extract(binary));
  chmodSync(placed, 0o755);
  writeFileSync(join(packages, target, NOTICES), extract(NOTICES));
  console.log(`${archive} -> ${placed}`);
}

if (failures.length > 0) {
  console.error(failures.join("\n"));
  process.exit(1);
}
