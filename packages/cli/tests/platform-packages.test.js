import { spawnSync } from "node:child_process";
import { mkdirSync, mkdtempSync, readFileSync, rmSync, statSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { afterEach, beforeEach, describe, expect, it } from "vitest";

const repo = join(dirname(fileURLToPath(import.meta.url)), "..", "..", "..");
const script = join(repo, "scripts", "platform-packages.mjs");

const every = [
  ["darwin", "amd64", "ocel_darwin_amd64_v1", "ocel"],
  ["darwin", "arm64", "ocel_darwin_arm64_v8.0", "ocel"],
  ["linux", "amd64", "ocel_linux_amd64_v1", "ocel"],
  ["linux", "arm64", "ocel_linux_arm64_v8.0", "ocel"],
  ["windows", "amd64", "ocel_windows_amd64_v1", "ocel.exe"],
];

let root = "";

function binary(goos, goarch, folder, name, id) {
  mkdirSync(join(root, "dist", folder), { recursive: true });
  writeFileSync(join(root, "dist", folder, name), `${id} ${goos} ${goarch}`);
  return {
    name,
    path: join("dist", folder, name),
    goos,
    goarch,
    type: "Binary",
    extra: { ID: id },
  };
}

function stage(targets, { from = root, dist = "dist", packages = "packages" } = {}) {
  const artifacts = targets.map(([goos, goarch, folder, name]) =>
    binary(goos, goarch, folder, name, "ocel"),
  );
  artifacts.push(
    binary("linux", "amd64", "provider-aws_linux_amd64_v1", "provider-aws", "provider-aws"),
  );
  writeFileSync(join(root, "dist", "artifacts.json"), JSON.stringify(artifacts));
  for (const [goos, goarch] of every) {
    const suffix = `${goos === "windows" ? "win32" : goos}-${goarch === "amd64" ? "x64" : goarch}`;
    mkdirSync(join(root, "packages", `cli-${suffix}`), { recursive: true });
  }
  return spawnSync(process.execPath, [script, dist, packages], { cwd: from, encoding: "utf8" });
}

beforeEach(() => {
  root = mkdtempSync(join(tmpdir(), "ocel-dist-"));
});

afterEach(() => {
  rmSync(root, { recursive: true, force: true });
});

describe("platform-packages.mjs", () => {
  it("fills every platform package with its executable binary", () => {
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

  it("leaves the provider binaries out of the npm packages", () => {
    stage(every);
    expect(() =>
      statSync(join(root, "packages", "cli-linux-x64", "bin", "provider-aws")),
    ).toThrow();
  });

  it("reads the manifest's paths against the project root, not the working directory", () => {
    const elsewhere = mkdtempSync(join(tmpdir(), "ocel-elsewhere-"));
    const run = stage(every, {
      from: elsewhere,
      dist: join(root, "dist"),
      packages: join(root, "packages"),
    });
    rmSync(elsewhere, { recursive: true, force: true });
    expect(run.stderr).toBe("");
    expect(run.status).toBe(0);
    expect(readFileSync(join(root, "packages", "cli-linux-x64", "bin", "ocel"), "utf8")).toBe(
      "ocel linux amd64",
    );
  });

  it("names the target goreleaser did not build", () => {
    const run = stage(every.filter(([goos, goarch]) => !(goos === "linux" && goarch === "arm64")));
    expect(run.status).not.toBe(0);
    expect(run.stderr).toContain("cli-linux-arm64");
  });
});
