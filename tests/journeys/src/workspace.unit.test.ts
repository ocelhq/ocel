import { describe, expect, it } from "bun:test";
import { specByName } from "./spec";
import {
  appCommand,
  appFolder,
  appHomes,
  migrateCommand,
  setSiteHostnames,
  stateComplaint,
} from "./workspace";

const workspace = specByName("sdk", "workspace");
const composite = specByName("sdk", "express");

describe("a multi-app row", () => {
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

  it("gives each app of a workspace its own env folder, and every other row none", () => {
    expect(workspace.apps.map((app) => appFolder(workspace, app))).toEqual(["/next", "/express"]);
    expect(appFolder(composite, "web")).toBeUndefined();
  });

  it("names the app directories that must hold no state of their own", () => {
    expect(appHomes(workspace)).toEqual(["apps/next", "apps/express"]);
    expect(appHomes(composite)).toEqual([]);
  });

  it("tells each app its own hostname, in its own folder, and skips an app that has none", async () => {
    const ran: string[][] = [];
    const run = async (_name: string, args: string[]) => {
      ran.push(args);
    };
    await setSiteHostnames(workspace, new Map([["next", "next-j.zone"]]), run);
    await setSiteHostnames(composite, new Map([["web", "web-j.zone"]]), run);
    expect(ran).toEqual([
      ["env", "set", "SITE_HOSTNAME", "next-j.zone", "--folder", "/next"],
      ["env", "set", "SITE_HOSTNAME", "web-j.zone"],
    ]);
  });
});

describe("the state a workspace writes", () => {
  const home = "/trees/sdk__workspace/sdk/workspace";

  it("is content with state under the config's own directory", () => {
    expect(stateComplaint(home, [home])).toBeUndefined();
  });

  it("complains when an app under it holds state", () => {
    expect(stateComplaint(home, [home, `${home}/apps/next`])).toMatch(/workspace\/apps\/next/);
  });

  it("complains when the config's directory holds none", () => {
    expect(stateComplaint(home, [])).toMatch(/holds no \.ocel state/);
  });
});
