import { execFileSync, spawnSync } from "node:child_process";
import { createHash } from "node:crypto";
import { mkdirSync, mkdtempSync, readFileSync, rmSync, statSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { afterEach, beforeEach, describe, expect, it } from "vitest";

const repo = join(dirname(fileURLToPath(import.meta.url)), "..", "..", "..");
const script = join(repo, "scripts", "platform-packages.mjs");

const version = "1.2.3";
const every = [
  ["darwin", "amd64"],
  ["darwin", "arm64"],
  ["linux", "amd64"],
  ["linux", "arm64"],
  ["windows", "amd64"],
];

let root = "";

function archive(goos, goarch) {
  const windows = goos === "windows";
  const name = `ocel_${version}_${goos}_${goarch}.${windows ? "zip" : "tar.gz"}`;
  const staging = mkdtempSync(join(tmpdir(), "ocel-archive-"));
  const binary = windows ? "ocel.exe" : "ocel";
  writeFileSync(join(staging, binary), `ocel ${goos} ${goarch}`);
  const out = join(root, "release", name);
  if (windows) {
    execFileSync("zip", ["-q", "-j", out, join(staging, binary)]);
  } else {
    execFileSync("tar", ["-czf", out, "-C", staging, binary]);
  }
  rmSync(staging, { recursive: true, force: true });
  return name;
}

function stage(targets, { tamper } = {}) {
  mkdirSync(join(root, "release"), { recursive: true });
  const lines = targets.map(([goos, goarch]) => {
    const name = archive(goos, goarch);
    const sum = createHash("sha256")
      .update(readFileSync(join(root, "release", name)))
      .digest("hex");
    return `${name === tamper ? "0".repeat(64) : sum}  ${name}`;
  });
  writeFileSync(join(root, "release", "checksums.txt"), `${lines.join("\n")}\n`);
  return spawnSync(
    process.execPath,
    [script, join(root, "release"), version, join(root, "packages")],
    { encoding: "utf8" },
  );
}

beforeEach(() => {
  root = mkdtempSync(join(tmpdir(), "ocel-release-"));
});

afterEach(() => {
  rmSync(root, { recursive: true, force: true });
});

describe("platform-packages.mjs", () => {
  it("fills every platform package with the executable binary its release archive contains", () => {
    const run = stage(every);
    expect(run.stderr).toBe("");
    expect(run.status).toBe(0);
    const placed = [
      ["cli-darwin-x64", "ocel", "ocel darwin amd64"],
      ["cli-darwin-arm64", "ocel", "ocel darwin arm64"],
      ["cli-linux-x64", "ocel", "ocel linux amd64"],
      ["cli-linux-arm64", "ocel", "ocel linux arm64"],
      ["cli-win32-x64", "ocel.exe", "ocel windows amd64"],
    ];
    for (const [pkg, name, body] of placed) {
      const path = join(root, "packages", pkg, "bin", name);
      expect(readFileSync(path, "utf8")).toBe(body);
      expect(statSync(path).mode & 0o111).toBeGreaterThan(0);
    }
  });

  it("places nothing from an archive checksums.txt does not vouch for", () => {
    const run = stage(every, { tamper: `ocel_${version}_linux_amd64.tar.gz` });
    expect(run.status).not.toBe(0);
    expect(run.stderr).toContain(`ocel_${version}_linux_amd64.tar.gz`);
    expect(() => statSync(join(root, "packages", "cli-linux-x64", "bin", "ocel"))).toThrow();
  });

  it("names the target the release has no archive for", () => {
    const run = stage(every.filter(([goos, goarch]) => !(goos === "linux" && goarch === "arm64")));
    expect(run.status).not.toBe(0);
    expect(run.stderr).toContain("cli-linux-arm64");
  });
});
