import { mkdtemp, mkdir, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import path from "node:path";
import { beforeAll, describe, expect, it } from "vitest";
import { appDirs, configTree, treeRoot } from "./ocel";
import { specByName } from "./spec";
import type { CellContext } from "./targets/types";
import { splitWorkspaceFile, workspaceClosure, workspaceFileFor } from "./tree";

const ROOT_WORKSPACE = `packages:
  - apps/*
  - packages/*
  - packages/native/*

allowBuilds:
  better-sqlite3: false

linkWorkspacePackages: true
`;

const LAYOUT: Record<string, unknown> = {
  "apps/web": {
    name: "@fake/web",
    dependencies: { "@fake/sdk": "workspace:^", express: "^5.0.0" },
    devDependencies: { "@fake/tooling": "workspace:*" },
  },
  "apps/worker": { name: "@fake/worker", dependencies: { "@fake/sdk": "workspace:^" } },
  "apps/unused": { name: "@fake/unused", dependencies: { "@fake/orphan": "workspace:^" } },
  "packages/sdk": {
    name: "@fake/sdk",
    optionalDependencies: { "@fake/native-linux": "workspace:^" },
    peerDependencies: { "@fake/runtime": "workspace:^" },
  },
  "packages/runtime": { name: "@fake/runtime" },
  "packages/tooling": { name: "@fake/tooling" },
  "packages/orphan": { name: "@fake/orphan" },
  "packages/native/linux": { name: "@fake/native-linux" },
  "packages/native/darwin": { name: "@fake/native-darwin" },
};

let root: string;

beforeAll(async () => {
  root = await mkdtemp(path.join(tmpdir(), "journey-closure-"));
  await writeFile(path.join(root, "pnpm-workspace.yaml"), ROOT_WORKSPACE);
  for (const [dir, manifest] of Object.entries(LAYOUT)) {
    await mkdir(path.join(root, dir), { recursive: true });
    await writeFile(path.join(root, dir, "package.json"), JSON.stringify(manifest));
  }
});

describe("the packages a tree has to carry", () => {
  it("reaches every workspace ref the apps declare, through any dependency field", async () => {
    expect(await workspaceClosure(root, ["apps/web"])).toEqual([
      "packages/native/linux",
      "packages/runtime",
      "packages/sdk",
      "packages/tooling",
    ]);
  });

  it("leaves out packages no app in the tree reaches", async () => {
    const held = await workspaceClosure(root, ["apps/web", "apps/worker"]);
    expect(held).not.toContain("packages/orphan");
    expect(held).not.toContain("packages/native/darwin");
  });

  it("never names an app as one of the packages beside it", async () => {
    expect(await workspaceClosure(root, ["apps/web", "apps/worker", "apps/unused"])).toContain(
      "packages/orphan",
    );
    expect(await workspaceClosure(root, ["apps/web", "apps/worker"])).not.toContain("apps/worker");
  });

  it("ignores a dependency that resolves from the registry rather than the workspace", async () => {
    expect(await workspaceClosure(root, ["apps/web"])).not.toContain("express");
  });
});

function cellFor(name: string): CellContext {
  const cell = { example: specByName(name), dir: "", slug: name, runId: "run", evidence: {} };
  return cell as unknown as CellContext;
}

describe("where an app sits in the tree built for it", () => {
  it("is a member directory of the synthetic root, whatever kind the example is", () => {
    for (const name of ["express", "workspace"]) {
      const cell = cellFor(name);
      expect(configTree(cell, "vps")).toBe(
        path.join(treeRoot(cell, "vps"), cell.example.dir),
      );
    }
  });

  it("brings the config's own directory and every sibling app it declares", () => {
    expect(appDirs(cellFor("express"))).toEqual(["express"]);
    expect(appDirs(cellFor("workspace"))).toEqual(["workspace", "next", "express", "hono"]);
  });
});

describe("the workspace file a tree gets", () => {
  it("carries every install-affecting key the repo root declares", () => {
    const carried = splitWorkspaceFile(ROOT_WORKSPACE);
    expect(carried.packages).toEqual(["apps/*", "packages/*", "packages/native/*"]);
    expect(carried.settings).toContain("better-sqlite3: false");
    expect(carried.settings).toContain("linkWorkspacePackages: true");
  });

  it("lists the members it was given and nothing the root globbed", () => {
    const written = workspaceFileFor(["web", "packages/sdk"], "linkWorkspacePackages: true\n");
    expect(written).toContain("  - web\n");
    expect(written).toContain("  - packages/sdk\n");
    expect(written).not.toContain("apps/*");
    expect(written).toContain("linkWorkspacePackages: true");
  });
});
