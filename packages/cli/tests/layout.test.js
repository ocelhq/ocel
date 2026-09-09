import { execFileSync } from "node:child_process";
import { cpSync, mkdirSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";

const packages = join(dirname(fileURLToPath(import.meta.url)), "..", "..");

function packed(source, prepare) {
  const staged = join(mkdtempSync(join(tmpdir(), "ocel-pack-")), "package");
  cpSync(source, staged, { recursive: true, filter: (path) => !path.includes("node_modules") });
  prepare?.(staged);
  const report = execFileSync("npm", ["pack", "--dry-run", "--json"], {
    cwd: staged,
    encoding: "utf8",
  });
  rmSync(dirname(staged), { recursive: true, force: true });
  const [entry] = JSON.parse(report);
  return { name: entry.name, files: entry.files.map((file) => file.path).sort() };
}

describe("the published @ocel/cli", () => {
  const tarball = packed(join(packages, "cli"));

  it("ships the launcher its bin points at, and nothing that only tests it", () => {
    expect(tarball.name).toBe("@ocel/cli");
    expect(tarball.files).toEqual(["README.md", "bin/ocel.js", "bin/resolve.js", "package.json"]);
  });
});

describe("a published platform package", () => {
  const tarball = packed(join(packages, "cli-linux-x64"), (staged) => {
    mkdirSync(join(staged, "bin"), { recursive: true });
    writeFileSync(join(staged, "bin", "ocel"), "", { mode: 0o755 });
  });

  it("ships the binary the launcher resolves, and nothing else", () => {
    expect(tarball.name).toBe("@ocel/cli-linux-x64");
    expect(tarball.files).toEqual(["bin/ocel", "package.json"]);
  });
});
