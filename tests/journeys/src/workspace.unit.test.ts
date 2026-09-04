import { readFile } from "node:fs/promises";
import path from "node:path";
import { describe, expect, it } from "vitest";
import { exampleDir } from "./paths";
import { specByName } from "./spec";
import { appCommand, appFolder, appHomes, migrateCommand, stateComplaint } from "./workspace";

const workspace = specByName("workspace");
const composite = specByName("express");

const TARGET_CONFIGS = ["ocel.aws.config.ts", "ocel.vps.config.ts"];

describe("a multi-app row", () => {
  it("reaches each app under apps/, and a single app where the config sits", () => {
    expect(appCommand(workspace, "express")).toEqual([
      "pnpm",
      "--dir",
      "apps/express",
      "run",
      "start",
    ]);
    expect(appCommand(workspace, "next")).toEqual(["pnpm", "--dir", "apps/next", "run", "start"]);
    expect(appCommand(composite, "web")).toEqual(["pnpm", "--dir", ".", "run", "start"]);
  });

  it("migrates where the config sits, since the schema belongs to the project", () => {
    expect(migrateCommand()).toEqual(["pnpm", "run", "migrate"]);
  });

  it("gives each app of a workspace its own env folder, and every other row none", () => {
    expect(workspace.apps.map((app) => appFolder(workspace, app))).toEqual(["/next", "/express"]);
    expect(appFolder(composite, "web")).toBeUndefined();
  });

  it("names the app directories that must hold no state of their own", () => {
    expect(appHomes(workspace)).toEqual(["apps/next", "apps/express"]);
    expect(appHomes(composite)).toEqual([]);
  });
});

describe("the state a workspace writes", () => {
  const home = "/trees/workspace/workspace";

  it("is content with state under the config's own directory", () => {
    expect(stateComplaint(home, [home])).toBeUndefined();
  });

  it("complains when an app under it holds state", () => {
    expect(stateComplaint(home, [home, "/trees/workspace/workspace/apps/next"])).toMatch(
      /workspace\/apps\/next/,
    );
  });

  it("complains when the config's directory holds none", () => {
    expect(stateComplaint(home, [])).toMatch(/holds no \.ocel state/);
  });
});

describe("the workspace row's target configs", () => {
  it("are byte-identical copies of a composite's", async () => {
    for (const name of TARGET_CONFIGS) {
      const mine = await readFile(path.join(exampleDir(workspace.dir), name));
      const theirs = await readFile(path.join(exampleDir(composite.dir), name));
      expect(mine.equals(theirs)).toBe(true);
    }
  });
});
