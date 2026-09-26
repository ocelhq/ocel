import { describe, expect, it } from "bun:test";
import { sdk } from "./matrix/fixtures";
import { appCommand, appHomes, migrateCommand, stateComplaint } from "./workspace";

const workspace = sdk.workspace;
const composite = sdk.node;

describe("a multi-app fixture", () => {
  it("reaches each app under apps/, and a single app where the config sits", () => {
    expect(appCommand(workspace, "express")).toEqual([
      "pnpm",
      "--dir",
      "apps/express",
      "run",
      "dev",
    ]);
    expect(appCommand(workspace, "next")).toEqual(["pnpm", "--dir", "apps/next", "run", "dev"]);
    expect(appCommand(composite, "web")).toEqual(["pnpm", "--dir", ".", "run", "dev"]);
  });

  it("migrates where the config sits, since the schema belongs to the project", () => {
    expect(migrateCommand()).toEqual(["pnpm", "run", "migrate"]);
  });

  it("names the app directories that must contain no state of their own", () => {
    expect(appHomes(workspace)).toEqual(["apps/next", "apps/express"]);
    expect(appHomes(composite)).toEqual([]);
  });
});

describe("the state a workspace writes", () => {
  const home = "/trees/sdk__workspace/sdk/workspace";

  it("is content with state under the config's own directory", () => {
    expect(stateComplaint(home, [home])).toBeUndefined();
  });

  it("complains when an app under it contains state", () => {
    expect(stateComplaint(home, [home, `${home}/apps/next`])).toMatch(/workspace\/apps\/next/);
  });

  it("complains when the config's directory contains none", () => {
    expect(stateComplaint(home, [])).toMatch(/contains no \.ocel state/);
  });
});
