import { spawnSync } from "node:child_process";
import { join } from "node:path";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { runLink } from "./cli.js";

vi.mock("node:child_process", () => ({ spawnSync: vi.fn() }));

const resolve = vi.fn();

vi.mock("node:module", () => ({ createRequire: () => ({ resolve }) }));

const run = vi.mocked(spawnSync);

const target = { project: "/repo/app", class: "production" } as const;

beforeEach(() => {
  vi.clearAllMocks();
  run.mockReturnValue({ status: 0, stderr: "" } as never);
  resolve.mockReturnValue("/repo/app/node_modules/ocel/package.json");
});

describe("reaching the ocel CLI", () => {
  it("runs the ocel the project itself resolves", () => {
    runLink(["ls"], target);

    expect(run).toHaveBeenCalledWith(
      process.execPath,
      [join("/repo/app/node_modules/ocel", "bin", "run.js"), "link", "ls"],
      expect.objectContaining({ cwd: "/repo/app" }),
    );
  });

  it("names the dependency the project is missing", () => {
    resolve.mockImplementation(() => {
      throw new Error("Cannot find module 'ocel/package.json'");
    });

    expect(() => runLink(["ls"], target)).toThrow(
      "@ocel/sst runs the ocel CLI in /repo/app, and ocel is not installed there. Add ocel to that project's dependencies.",
    );
  });

  it("says so when the CLI dies without a word", () => {
    run.mockReturnValue({ status: 3, stderr: "  \n" } as never);

    expect(() => runLink(["ls"], target)).toThrow(/exited 3 without saying why/);
  });

  it("refuses a target no link could be published to before spawning anything", () => {
    expect(() =>
      runLink(["ls"], { ...target, project: "" }),
    ).toThrow(/an ocel project is required/);
    expect(run).not.toHaveBeenCalled();
  });
});
