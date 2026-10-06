import { mkdtempSync, rmSync, writeFileSync } from "node:fs";
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
    deploymentId: "0123456789abcdef0123456789abcdef",
    nextRuntimeDir: "/var/host/next",
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

  it("points Next at the adapter and the deployment id it builds under", async () => {
    const env = await envOf(app({ deploymentId: "fedcba9876543210fedcba9876543210" }));
    expect(env.NEXT_ADAPTER_PATH).toBe(ADAPTER);
    expect(env.NEXT_DEPLOYMENT_ID).toBe("fedcba9876543210fedcba9876543210");
  });

  for (const owned of ["NEXT_ADAPTER_PATH", "NEXT_DEPLOYMENT_ID"]) {
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

  it("passes the directory the host loads Next's runtime files from into the build", async () => {
    const env = await envOf(app({ nextRuntimeDir: "/opt/elsewhere/next" }));
    expect(env.OCEL_NEXT_RUNTIME_DIR).toBe("/opt/elsewhere/next");
  });

  it("names no Next runtime directory when the build request names none, for the adapter to refuse", async () => {
    const { nextRuntimeDir: _, ...unnamed } = app();
    const env = await envOf(unnamed);
    expect(env.OCEL_NEXT_RUNTIME_DIR).toBe("");
  });

  it("passes the host's per-function size budget into the build", async () => {
    const env = await envOf(app({ maxFunctionBytes: 209715200 }));
    expect(env.OCEL_MAX_FUNCTION_BYTES).toBe("209715200");
  });

  it("sets no size budget when the host declares none", async () => {
    expect((await envOf(app())).OCEL_MAX_FUNCTION_BYTES).toBe("");
  });

  it("tells the build when its host refreshes by request", async () => {
    const env = await envOf(app({ nextRefreshesByRequest: true }));
    expect(env.OCEL_NEXT_REFRESHES_BY_REQUEST).toBe("1");
  });

  it("tells the build nothing when its host refreshes in the background", async () => {
    expect((await envOf(app())).OCEL_NEXT_REFRESHES_BY_REQUEST).toBe("");
  });

  it("builds for production whatever NODE_ENV the shell sets", async () => {
    expect((await envOf(app({ env: { NODE_ENV: "development" } }))).NODE_ENV).toBe("production");
  });

  it("binds an app that declares no folder to the project root", async () => {
    expect((await envOf(app())).OCEL_APP_FOLDER).toBe("");
  });
});
