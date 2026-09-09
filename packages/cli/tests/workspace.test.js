import { globSync, readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";
import { platformPackage } from "../bin/resolve.js";

const repo = join(dirname(fileURLToPath(import.meta.url)), "..", "..", "..");

function read(...parts) {
  return JSON.parse(readFileSync(join(repo, ...parts), "utf8"));
}

function workspacePackages() {
  const globs = readFileSync(join(repo, "pnpm-workspace.yaml"), "utf8")
    .split(/\r?\n/)
    .slice(1)
    .reduce(
      (collected, line) => {
        if (collected.done || (line.trim() !== "" && !line.startsWith("  - "))) {
          return { ...collected, done: true };
        }
        return line.trim() === ""
          ? collected
          : { ...collected, globs: [...collected.globs, `${line.slice(4)}/package.json`] };
      },
      { globs: [], done: false },
    ).globs;
  return globSync(globs, { cwd: repo, exclude: (path) => path.includes("node_modules") }).map(
    (path) => JSON.parse(readFileSync(join(repo, path), "utf8")).name,
  );
}

const platforms = [
  ["darwin", "arm64"],
  ["darwin", "x64"],
  ["linux", "arm64"],
  ["linux", "x64"],
  ["win32", "x64"],
].map(([platform, arch]) => platformPackage(platform, arch));

describe("the ocel package", () => {
  const manifest = read("packages", "ocel", "package.json");

  it("ships no binary", () => {
    expect(manifest.bin).toBeUndefined();
    expect(manifest.files).not.toContain("bin/");
    expect(Object.keys(manifest.optionalDependencies ?? {})).toEqual([]);
  });

  it("keeps the runtime and config authoring exports", () => {
    expect(Object.keys(manifest.exports)).toEqual(
      expect.arrayContaining(["./config", "./edge", "./dns", "./env", "./blob", "./postgres"]),
    );
  });
});

describe("the workspace", () => {
  const names = workspacePackages();

  it("holds no provider package", () => {
    expect(names.filter((name) => name?.startsWith("@ocel/provider-"))).toEqual([]);
  });

  it("holds the cli wrapper and every platform package", () => {
    expect(names).toEqual(expect.arrayContaining(["@ocel/cli", ...platforms]));
  });
});

describe("the changesets fixed group", () => {
  const [group, ...rest] = read(".changeset", "config.json").fixed;

  it("bumps the sdk, the wrapper and every platform package together", () => {
    expect([...group].sort()).toEqual(["@ocel/cli", ...platforms, "ocel"].sort());
    expect(rest).toEqual([]);
  });
});
