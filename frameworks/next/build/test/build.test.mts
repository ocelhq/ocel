import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";
import { afterAll, afterEach, describe, expect, it } from "vitest";
import { buildNext, buildProcess, type NextBuild } from "../src/build.mjs";

const roots: string[] = [];
afterAll(() => {
  for (const d of roots) rmSync(d, { recursive: true, force: true });
});

const realSpawn = buildProcess.spawn;
afterEach(() => {
  buildProcess.spawn = realSpawn;
});

const ADAPTER = "/dist/next-adapter/next-adapter.mjs";

function nextApp(
  pkg: unknown = { scripts: { build: "next build" }, dependencies: { next: "16" } },
) {
  const dir = mkdtempSync(path.join(tmpdir(), "next-"));
  roots.push(dir);
  writeFileSync(path.join(dir, "package.json"), JSON.stringify(pkg));
  return dir;
}

function app(overrides: Partial<NextBuild> = {}): NextBuild {
  return {
    name: "web",
    cwd: nextApp(),
    outputDir: "/out/apps/web",
    buildId: "0123456789abcdef0123456789abcdef",
    ...overrides,
  };
}

async function envOf(build: NextBuild): Promise<Record<string, string>> {
  let env: Record<string, string> = {};
  buildProcess.spawn = async (_command, _args, _cwd, e) => void (env = e);
  await buildNext(build, ADAPTER);
  return env;
}

describe("buildNext", () => {
  it("throws when there is no build script", async () => {
    const cwd = nextApp({ dependencies: { next: "16" } });
    await expect(buildNext(app({ cwd }), ADAPTER)).rejects.toThrow(/no "build" script/);
  });

  it("runs the resolved build command in the app's directory", async () => {
    const calls: string[][] = [];
    buildProcess.spawn = async (command, args, cwd) => void calls.push([cwd, command, ...args]);
    const build = app();

    await buildNext(build, ADAPTER);

    expect(calls).toHaveLength(1);
    expect(calls[0]?.[0]).toBe(build.cwd);
    expect(calls[0]).toContain("run");
    expect(calls[0]).toContain("build");
  });

  it("passes the app name to the build as OCEL_APP_NAME", async () => {
    expect((await envOf(app({ name: "marketing" }))).OCEL_APP_NAME).toBe("marketing");
  });

  it("passes the app's own output subtree to the build as OCEL_OUTPUT_DIR", async () => {
    const env = await envOf(app({ outputDir: "/out/apps/marketing" }));
    expect(env.OCEL_OUTPUT_DIR).toBe("/out/apps/marketing");
  });

  it("points Next at the adapter and the build id it builds under", async () => {
    const env = await envOf(app({ buildId: "fedcba9876543210fedcba9876543210" }));
    expect(env.NEXT_ADAPTER_PATH).toBe(ADAPTER);
    expect(env.NEXT_DEPLOYMENT_ID).toBe("fedcba9876543210fedcba9876543210");
  });

  for (const owned of [
    "NEXT_ADAPTER_PATH",
    "NEXT_DEPLOYMENT_ID",
    "NODE_ENV",
    "OCEL_APP_NAME",
    "OCEL_OUTPUT_DIR",
    "OCEL_APP_FOLDER",
    "OCEL_EDGE_KIND",
    "OCEL_ALLOW_DEGRADED",
    "OCEL_MAX_FUNCTION_BYTES",
  ]) {
    it(`refuses a variable declared as ${owned} before anything runs`, async () => {
      let ran = false;
      buildProcess.spawn = async () => void (ran = true);

      await expect(buildNext(app({ env: { [owned]: "hijacked" } }), ADAPTER)).rejects.toThrow(
        owned,
      );
      expect(ran).toBe(false);
    });
  }

  it("builds each app with its own values and its own folder binding", async () => {
    const store = await envOf(app({ folder: "/storefront", env: { POSTHOG_ID: "ph-store" } }));
    const admin = await envOf(app({ folder: "/admin", env: { POSTHOG_ID: "ph-admin" } }));

    expect(store.POSTHOG_ID).toBe("ph-store");
    expect(store.OCEL_APP_FOLDER).toBe("/storefront");
    expect(admin.POSTHOG_ID).toBe("ph-admin");
    expect(admin.OCEL_APP_FOLDER).toBe("/admin");
  });

  it("passes the edge kind and the waived needs into the build", async () => {
    const env = await envOf(
      app({ edgeKind: "cloudfront", allowDegraded: ["edge-middleware", "edge-runtime"] }),
    );
    expect(env.OCEL_EDGE_KIND).toBe("cloudfront");
    expect(env.OCEL_ALLOW_DEGRADED).toBe("edge-middleware,edge-runtime");
  });

  it("waives nothing when the build request names no edge", async () => {
    const env = await envOf(app());
    expect(env.OCEL_EDGE_KIND).toBe("");
    expect(env.OCEL_ALLOW_DEGRADED).toBe("");
  });

  it("passes the host's per-function size budget into the build", async () => {
    const env = await envOf(app({ maxFunctionBytes: 209715200 }));
    expect(env.OCEL_MAX_FUNCTION_BYTES).toBe("209715200");
  });

  it("sets no size budget when the host declares none", async () => {
    expect((await envOf(app())).OCEL_MAX_FUNCTION_BYTES).toBe("");
  });

  it("builds for production whatever NODE_ENV the shell sets", async () => {
    process.env.NODE_ENV = "development";
    try {
      expect((await envOf(app())).NODE_ENV).toBe("production");
    } finally {
      delete process.env.NODE_ENV;
    }
  });

  it("binds an app that declares no folder to the project root", async () => {
    expect((await envOf(app())).OCEL_APP_FOLDER).toBe("");
  });

  it("builds with none of the names the request unsets, whatever the shell holds", async () => {
    process.env.SESSION_SECRET = "stale-from-the-shell";
    process.env.OCEL_VAR_POSTHOG_ID = "stale-from-the-shell";
    try {
      const env = await envOf(
        app({
          env: { POSTHOG_ID: "ph-web" },
          unset: ["OCEL_VAR_POSTHOG_ID", "SESSION_SECRET"],
        }),
      );
      expect(env).not.toHaveProperty("SESSION_SECRET");
      expect(env).not.toHaveProperty("OCEL_VAR_POSTHOG_ID");
      expect(env.POSTHOG_ID).toBe("ph-web");
      expect(env.PATH).toBe(process.env.PATH);
    } finally {
      delete process.env.SESSION_SECRET;
      delete process.env.OCEL_VAR_POSTHOG_ID;
    }
  });

  describe("when the node the app's build runs on has no process.getBuiltinModule, which the SDK reads a live dir with", () => {
    const nodeOf = buildProcess.node;
    afterEach(() => {
      buildProcess.node = nodeOf;
    });

    it("refuses a build handed a live dir before anything runs, naming the node it found and the ones it needs", async () => {
      const asked: { cwd: string; path?: string }[] = [];
      buildProcess.node = async (cwd, env) => {
        asked.push({ cwd, path: env.PATH });
        return { version: "20.11.0", readsLiveDir: false };
      };
      let ran = false;
      buildProcess.spawn = async () => void (ran = true);
      const build = app({ env: { OCEL_LIVE_DIR: "/tmp/ocel-live-1", PATH: "/app/bin" } });

      const refused = buildNext(build, ADAPTER);

      await expect(refused).rejects.toThrow(/Node 20\.16\+ or 22\.3\+/);
      await expect(refused).rejects.toThrow(/20\.11\.0/);
      expect(ran).toBe(false);
      expect(asked).toEqual([{ cwd: build.cwd, path: "/app/bin" }]);
    });

    it("builds an app handed no live dir without asking which node it runs on", async () => {
      buildProcess.node = async () => {
        throw new Error("asked which node builds an app with no live dir");
      };
      await expect(envOf(app({ env: { OCEL_LIVE_DIR: "" } }))).resolves.toBeDefined();
    });
  });

  it.skipIf(process.platform === "win32")(
    "asks the node a workspace's node_modules/.bin puts first on the build script's PATH, not the shell's",
    async () => {
      const root = nextApp();
      const bin = path.join(root, "node_modules", ".bin");
      mkdirSync(bin, { recursive: true });
      writeFileSync(path.join(bin, "node"), '#!/bin/sh\necho "22.3.0 function"\n', {
        mode: 0o755,
      });
      const cwd = path.join(root, "apps", "web");
      mkdirSync(cwd, { recursive: true });

      const node = await buildProcess.node(cwd, { PATH: process.env.PATH ?? "" });

      expect(node).toEqual({ version: "22.3.0", readsLiveDir: true });
    },
  );

  it("builds an app handed a live dir on a node whose process.getBuiltinModule the SDK reads it with", async () => {
    const nodeOf = buildProcess.node;
    buildProcess.node = async () => ({ version: "22.3.0", readsLiveDir: true });
    try {
      await expect(
        envOf(app({ env: { OCEL_LIVE_DIR: "/tmp/ocel-live-1" } })),
      ).resolves.toBeDefined();
    } finally {
      buildProcess.node = nodeOf;
    }
  });
});
