import { mkdirSync, mkdtempSync, realpathSync, rmSync, symlinkSync, writeFileSync } from "node:fs";
import { createRequire } from "node:module";
import { tmpdir } from "node:os";
import path from "node:path";
import { Refusal } from "@framework/node-build/refusal";
import { buildProcess } from "@framework/node-build/script";
import { afterAll, afterEach, describe, expect, it } from "vitest";
import { buildNext, type NextBuild } from "../src/build.mjs";

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

function outputDir(): string {
  const dir = mkdtempSync(path.join(tmpdir(), "next-out-"));
  roots.push(dir);
  return dir;
}

function app(overrides: Partial<NextBuild> = {}): NextBuild {
  return {
    name: "web",
    cwd: nextApp(),
    outputDir: outputDir(),
    buildId: "0123456789abcdef0123456789abcdef",
    ...overrides,
  };
}

function writeNextHosting(env: Record<string, string>): void {
  writeFileSync(
    path.join(env.OCEL_OUTPUT_DIR ?? "", "hosting.json"),
    '{"version":1,"framework":"next"}',
  );
}

async function envOf(build: NextBuild): Promise<Record<string, string>> {
  let env: Record<string, string> = {};
  buildProcess.spawn = async (_command, _args, _cwd, e) => {
    env = e;
    writeNextHosting(e);
  };
  await buildNext(build, ADAPTER);
  return env;
}

describe("buildNext", () => {
  it("throws when there is no build script", async () => {
    const cwd = nextApp({ dependencies: { next: "16" } });
    const refused = buildNext(app({ cwd }), ADAPTER);

    await expect(refused).rejects.toThrow(/no "build" script/);
    await expect(refused).rejects.toBeInstanceOf(Refusal);
  });

  it("names the next release that runs the adapter when the build wrote no hosting", async () => {
    buildProcess.spawn = async () => {};

    await expect(buildNext(app(), ADAPTER)).rejects.toThrow(
      /nothing wrote hosting\.json.*upgrade next to 16\.2\.10/,
    );
  });

  it("runs the resolved build command in the app's directory", async () => {
    const calls: string[][] = [];
    buildProcess.spawn = async (command, args, cwd, env) => {
      calls.push([cwd, command, ...args]);
      writeNextHosting(env);
    };
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
    const marketing = outputDir();
    const env = await envOf(app({ outputDir: marketing }));
    expect(env.OCEL_OUTPUT_DIR).toBe(marketing);
  });

  it("points Next at the adapter and the build id it builds under", async () => {
    const env = await envOf(app({ buildId: "fedcba9876543210fedcba9876543210" }));
    expect(env.NEXT_ADAPTER_PATH).toBe(ADAPTER);
    expect(env.NEXT_DEPLOYMENT_ID).toBe("fedcba9876543210fedcba9876543210");
  });

  it("hands the adapter the declared public keys as OCEL_PUBLIC_KEYS", async () => {
    const env = await envOf(app({ publicKeys: ["NEXT_PUBLIC_API_URL", "NEXT_PUBLIC_RETRIES"] }));
    expect(env.OCEL_PUBLIC_KEYS).toBe("NEXT_PUBLIC_API_URL,NEXT_PUBLIC_RETRIES");
  });

  it("hands the adapter no public keys when the app declared none", async () => {
    expect((await envOf(app())).OCEL_PUBLIC_KEYS).toBe("");
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
    "OCEL_PUBLIC_KEYS",
  ]) {
    it(`refuses a variable declared as ${owned} before anything runs`, async () => {
      let ran = false;
      buildProcess.spawn = async () => void (ran = true);

      const refused = buildNext(app({ env: { [owned]: "hijacked" } }), ADAPTER);

      await expect(refused).rejects.toThrow(owned);
      await expect(refused).rejects.toBeInstanceOf(Refusal);
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
    process.env.STRIPE_API_KEY = "stale-from-the-shell";
    try {
      const env = await envOf(
        app({
          env: { POSTHOG_ID: "ph-web" },
          unset: ["STRIPE_API_KEY", "SESSION_SECRET"],
        }),
      );
      expect(env).not.toHaveProperty("SESSION_SECRET");
      expect(env).not.toHaveProperty("STRIPE_API_KEY");
      expect(env.POSTHOG_ID).toBe("ph-web");
      expect(env.PATH).toBe(process.env.PATH);
    } finally {
      delete process.env.SESSION_SECRET;
      delete process.env.STRIPE_API_KEY;
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
      await expect(refused).rejects.toBeInstanceOf(Refusal);
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

  describe("when the next.config next build loads names the adapter it runs", () => {
    const nextPackage = path.dirname(createRequire(import.meta.url).resolve("next/package.json"));

    function ocelAdapter(): string {
      const dir = mkdtempSync(path.join(tmpdir(), "ocel-adapter-"));
      roots.push(dir);
      const file = path.join(dir, "next-adapter.mjs");
      writeFileSync(file, 'export default { name: "ocel" };\n');
      return file;
    }

    function configuredApp(file: string, config: string): string {
      const cwd = nextApp();
      mkdirSync(path.join(cwd, "node_modules"));
      symlinkSync(nextPackage, path.join(cwd, "node_modules", "next"), "dir");
      writeFileSync(path.join(cwd, "my-adapter.js"), 'module.exports = { name: "mine" };\n');
      writeFileSync(path.join(cwd, file), config);
      return cwd;
    }

    function build(cwd: string, adapter: string, env?: Record<string, string>) {
      let ran = false;
      buildProcess.spawn = async (_command, _args, _cwd, e) => {
        ran = true;
        writeNextHosting(e);
      };
      const result = buildNext(app({ cwd, env }), adapter).then(() => ran);
      return { result, ran: () => ran };
    }

    it("builds an app whose adapterPath defers to NEXT_ADAPTER_PATH", async () => {
      const cwd = configuredApp(
        "next.config.js",
        "module.exports = { adapterPath: process.env.NEXT_ADAPTER_PATH ?? require.resolve('./my-adapter.js') };\n",
      );

      await expect(build(cwd, ocelAdapter()).result).resolves.toBe(true);
    });

    it("builds an app whose adapterPath reaches ocel's adapter through a symlink", async () => {
      const adapter = ocelAdapter();
      const links = nextApp();
      const linked = path.join(links, "linked-adapter.mjs");
      symlinkSync(adapter, linked);
      const cwd = configuredApp(
        "next.config.js",
        `module.exports = { adapterPath: ${JSON.stringify(linked)} };\n`,
      );

      await expect(build(cwd, adapter).result).resolves.toBe(true);
    });

    for (const [form, file, config] of [
      [
        "an object literal",
        "next.config.js",
        "module.exports = { adapterPath: require.resolve('./my-adapter.js') };\n",
      ],
      [
        "an assignment",
        "next.config.js",
        "const nextConfig = {};\nnextConfig.adapterPath = require.resolve('./my-adapter.js');\nmodule.exports = nextConfig;\n",
      ],
      [
        "a TypeScript config",
        "next.config.ts",
        "import path from 'node:path';\nexport default { adapterPath: path.join(process.cwd(), 'my-adapter.js') };\n",
      ],
    ] as const) {
      it(`refuses an app that names its own adapter in ${form} before next build runs`, async () => {
        const cwd = configuredApp(file, config);

        const { result, ran } = build(cwd, ocelAdapter());

        await expect(result).rejects.toThrow(
          `app "web" sets adapterPath to ${realpathSync(path.join(cwd, "my-adapter.js"))} in ${file}, and next build then runs that adapter in place of ocel's, which writes the output ocel deploys: delete adapterPath from ${file}`,
        );
        await expect(result).rejects.toBeInstanceOf(Refusal);
        expect(ran()).toBe(false);
      });
    }

    it("refuses an adapterPath read from a value the app builds with", async () => {
      const cwd = configuredApp(
        "next.config.js",
        "module.exports = { adapterPath: process.env.APP_ADAPTER };\n",
      );

      const { result, ran } = build(cwd, ocelAdapter(), {
        APP_ADAPTER: path.join(cwd, "my-adapter.js"),
      });

      await expect(result).rejects.toThrow(
        /sets adapterPath to \S*my-adapter\.js in next\.config\.js/,
      );
      expect(ran()).toBe(false);
    });
  });
});
