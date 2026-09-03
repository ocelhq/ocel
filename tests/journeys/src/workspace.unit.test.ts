import { readFile } from "node:fs/promises";
import path from "node:path";
import { describe, expect, it } from "vitest";
import { exampleDir } from "./paths";
import { specByName } from "./spec";
import {
  appCommand,
  appFolder,
  migrateCommand,
  siblingDirs,
  stateComplaint,
} from "./workspace";

const workspace = specByName("workspace");
const composite = specByName("express");

const TARGET_CONFIGS = ["ocel.aws.config.ts", "ocel.vps.config.ts"];

describe("a workspace row", () => {
  it("reaches each app through the relative path its config declares", () => {
    expect(appCommand(workspace, "hono")).toEqual([
      "pnpm",
      "--dir",
      "../hono",
      "run",
      "start",
    ]);
    expect(appCommand(composite, "web")).toEqual(["pnpm", "--dir", ".", "run", "start"]);
  });

  it("migrates through its first app, which owns the schema the three share", () => {
    expect(migrateCommand(workspace)).toEqual([
      "pnpm",
      "--dir",
      "../next",
      "run",
      "migrate",
    ]);
    expect(migrateCommand(composite)).toEqual(["pnpm", "--dir", ".", "run", "migrate"]);
  });

  it("gives each app its own env folder and a composite none", () => {
    expect(workspace.apps.map((app) => appFolder(workspace, app))).toEqual([
      "/next",
      "/express",
      "/hono",
    ]);
    expect(appFolder(composite, "web")).toBeUndefined();
  });

  it("brings the apps it declares by path into its work tree", () => {
    expect(siblingDirs(workspace)).toEqual(["next", "express", "hono"]);
    expect(siblingDirs(composite)).toEqual([]);
  });
});

describe("the state a workspace writes", () => {
  const home = "/trees/workspace/workspace";

  it("is content with state under the config's own directory", () => {
    expect(stateComplaint(home, [home])).toBeUndefined();
  });

  it("complains when a sibling app holds state", () => {
    expect(stateComplaint(home, [home, "/trees/workspace/next"])).toMatch(
      /trees\/workspace\/next/,
    );
  });

  it("complains when the config's directory holds none", () => {
    expect(stateComplaint(home, [])).toMatch(/holds no \.ocel state/);
  });
});

describe("the workspace's target configs", () => {
  it("are byte-identical copies of a composite's", async () => {
    for (const name of TARGET_CONFIGS) {
      const mine = await readFile(path.join(exampleDir(workspace.dir), name));
      const theirs = await readFile(path.join(exampleDir(composite.dir), name));
      expect(mine.equals(theirs)).toBe(true);
    }
  });
});
