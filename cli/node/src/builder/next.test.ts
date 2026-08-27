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

  it("passes the app's own output subtree to the build as OCEL_OUTPUT_DIR", async () => {
    const dir = nextApp({ scripts: { build: "next build" }, dependencies: { next: "16" } });
    let env: Record<string, string> | undefined;
    nextRunner.run = async (_command, _args, _cwd, e) => void (env = e);

    await buildNext({ name: "marketing", cwd: dir }, { outDir: "/out" });

    expect(env?.OCEL_OUTPUT_DIR).toBe(path.join("/out", "apps", "marketing"));
  });

  it("builds each app with its own values and its own folder binding", async () => {
    const envs: Record<string, Record<string, string> | undefined> = {};
    nextRunner.run = async (_command, _args, _cwd, e) => void (envs[e?.OCEL_APP_NAME ?? ""] = e);

    for (const [name, folder, value] of [
      ["storefront", "/storefront", "ph-store"],
      ["admin", "/admin", "ph-admin"],
    ]) {
      const dir = nextApp({ scripts: { build: "next build" }, dependencies: { next: "16" } });
      await buildNext({ name, cwd: dir, folder, env: { POSTHOG_ID: value } }, { outDir: "/out" });
    }

    expect(envs.storefront?.POSTHOG_ID).toBe("ph-store");
    expect(envs.storefront?.OCEL_APP_FOLDER).toBe("/storefront");
    expect(envs.admin?.POSTHOG_ID).toBe("ph-admin");
    expect(envs.admin?.OCEL_APP_FOLDER).toBe("/admin");
  });

  it("passes the edge kind and the waived needs into the build", async () => {
    const dir = nextApp({ scripts: { build: "next build" }, dependencies: { next: "16" } });
    let env: Record<string, string> | undefined;
    nextRunner.run = async (_command, _args, _cwd, e) => void (env = e);

    await buildNext(
      { name: "web", cwd: dir },
      { outDir: "/out", edgeKind: "cloudfront", allowDegraded: ["edge-middleware", "edge-runtime"] },
    );

    expect(env?.OCEL_EDGE_KIND).toBe("cloudfront");
    expect(env?.OCEL_ALLOW_DEGRADED).toBe("edge-middleware,edge-runtime");
  });

  it("waives nothing when the build request names no edge", async () => {
    const dir = nextApp({ scripts: { build: "next build" }, dependencies: { next: "16" } });
    let env: Record<string, string> | undefined;
    nextRunner.run = async (_command, _args, _cwd, e) => void (env = e);

    await buildNext({ name: "web", cwd: dir }, { outDir: "/out" });

    expect(env?.OCEL_EDGE_KIND).toBe("");
    expect(env?.OCEL_ALLOW_DEGRADED).toBe("");
  });

  it("builds under NODE_ENV=production regardless of the host shell's value", async () => {
    const dir = nextApp({ scripts: { build: "next build" }, dependencies: { next: "16" } });
    let env: Record<string, string> | undefined;
    nextRunner.run = async (_command, _args, _cwd, e) => void (env = e);

    await buildNext({ name: "web", cwd: dir }, { outDir: "/out" });

    expect(env?.NODE_ENV).toBe("production");
  });

  it("lets the app's declared env override NODE_ENV", async () => {
    const dir = nextApp({ scripts: { build: "next build" }, dependencies: { next: "16" } });
    let env: Record<string, string> | undefined;
    nextRunner.run = async (_command, _args, _cwd, e) => void (env = e);

    await buildNext({ name: "web", cwd: dir, env: { NODE_ENV: "test" } }, { outDir: "/out" });

    expect(env?.NODE_ENV).toBe("test");
  });

  it("binds an app that declares no folder to the project root", async () => {
    const dir = nextApp({ scripts: { build: "next build" }, dependencies: { next: "16" } });
    let env: Record<string, string> | undefined;
    nextRunner.run = async (_command, _args, _cwd, e) => void (env = e);

    await buildNext({ name: "web", cwd: dir }, { outDir: "/out" });

    expect(env?.OCEL_APP_FOLDER).toBe("");
  });
});
