import { mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";
import { afterAll, afterEach, describe, expect, it } from "vitest";
import { buildNext, nextRunner } from "./next.js";

const roots: string[] = [];
afterAll(() => roots.forEach((d) => rmSync(d, { recursive: true, force: true })));

const realRun = nextRunner.run;
afterEach(() => (nextRunner.run = realRun));

function nextApp(pkg: unknown): string {
  const dir = mkdtempSync(path.join(tmpdir(), "next-"));
  roots.push(dir);
  writeFileSync(path.join(dir, "package.json"), JSON.stringify(pkg));
  return dir;
}

describe("buildNext", () => {
  it("throws when there is no build script", async () => {
    const dir = nextApp({ dependencies: { next: "16" } });
    await expect(buildNext({ name: "web", cwd: dir }, { outDir: dir })).rejects.toThrow(/no "build" script/);
  });

  it("runs the resolved build command and emits no function", async () => {
    const dir = nextApp({ scripts: { build: "next build" }, dependencies: { next: "16" } });
    const calls: string[][] = [];
    nextRunner.run = async (command, args) => void calls.push([command, ...args]);

    const summaries = await buildNext({ name: "web", cwd: dir }, { outDir: dir });

    expect(summaries).toEqual([]);
    expect(calls).toHaveLength(1);
    expect(calls[0]).toContain("run");
    expect(calls[0]).toContain("build");
  });

  it("passes the app name to the build as OCEL_APP_NAME", async () => {
    const dir = nextApp({ scripts: { build: "next build" }, dependencies: { next: "16" } });
    let env: Record<string, string> | undefined;
    nextRunner.run = async (_command, _args, _cwd, e) => void (env = e);

    await buildNext({ name: "marketing", cwd: dir }, { outDir: dir });

    expect(env?.OCEL_APP_NAME).toBe("marketing");
  });
});
