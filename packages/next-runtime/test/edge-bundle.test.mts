import { mkdtemp, mkdir, writeFile, readFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { afterEach, expect, test, vi } from "vitest";

import adapter from "../src/next-adapter.mts";

afterEach(() => {
  vi.unstubAllEnvs();
  vi.restoreAllMocks();
});

const buildEnv = {
  __NEXT_BUILD_ID: "test-build",
  __NEXT_PREVIEW_MODE_ID: "preview",
};

// A synthetic build result standing in for a real edge build: two variants of
// one page sharing an entry key, an app route with its own, edge middleware
// carrying matchers and a wasm asset, and one nodejs route so the lambda path
// stays exercised alongside. Chunk contents are chosen to force the interesting
// cases — two distinct asset keys holding identical bytes, one asset shared by
// every output, and a source map that must not reach the bundle.
async function synthEdgeProject() {
  const projectDir = await mkdtemp(join(tmpdir(), "ocel-next-edge-"));

  const chunkDir = join(projectDir, ".next/server/edge/chunks");
  await mkdir(chunkDir, { recursive: true });
  const chunks = {
    "chunks/dup-a.js": "DUPLICATE",
    "chunks/dup-b.js": "DUPLICATE",
    "chunks/mw.js": "MW",
    "chunks/page.js": "PAGE",
    "chunks/page.js.map": '{"version":3}',
    "chunks/route.js": "ROUTE",
    "chunks/shared.js": "SHARED",
  };
  const abs: Record<string, string> = {};
  for (const [key, body] of Object.entries(chunks)) {
    abs[key] = join(projectDir, ".next/server/edge", key);
    await writeFile(abs[key]!, body);
  }

  const wasmPath = join(projectDir, ".next/server/edge/wasm/hello.wasm");
  await mkdir(dirname(wasmPath), { recursive: true });
  await writeFile(wasmPath, Buffer.from([0, 97, 115, 109]));

  const nodeHandler = join(projectDir, ".next/server/app/api/docs/route.js");
  await mkdir(dirname(nodeHandler), { recursive: true });
  await writeFile(nodeHandler, "module.exports = () => {}");

  const pageAssets = {
    "chunks/dup-a.js": abs["chunks/dup-a.js"]!,
    "chunks/page.js": abs["chunks/page.js"]!,
    "chunks/page.js.map": abs["chunks/page.js.map"]!,
    "chunks/shared.js": abs["chunks/shared.js"]!,
  };
  const pageEdgeRuntime = {
    modulePath: abs["chunks/page.js"]!,
    entryKey: "middleware_app/edge-page/page",
    handlerExport: "handler",
  };

  const args = {
    routing: {
      beforeMiddleware: [],
      beforeFiles: [],
      afterFiles: [],
      dynamicRoutes: [],
      onMatch: [],
      fallback: [],
    },
    outputs: {
      pages: [],
      pagesApi: [],
      appPages: [
        {
          pathname: "/edge-page",
          id: "app/edge-page/page",
          sourcePage: "/edge-page/page",
          assets: pageAssets,
          wasmAssets: {},
          runtime: "edge",
          filePath: abs["chunks/page.js"]!,
          edgeRuntime: pageEdgeRuntime,
          config: { env: buildEnv },
          type: "APP_PAGE",
        },
        {
          // The `.rsc` variant is its own output sharing the compiled entry.
          pathname: "/edge-page.rsc",
          id: "app/edge-page/page.rsc",
          sourcePage: "/edge-page/page",
          assets: pageAssets,
          wasmAssets: {},
          runtime: "edge",
          filePath: abs["chunks/page.js"]!,
          edgeRuntime: pageEdgeRuntime,
          config: { env: buildEnv },
          type: "APP_PAGE",
        },
      ],
      appRoutes: [
        {
          pathname: "/api/edge",
          id: "app/api/edge/route",
          sourcePage: "/api/edge/route",
          assets: {
            "chunks/dup-b.js": abs["chunks/dup-b.js"]!,
            "chunks/route.js": abs["chunks/route.js"]!,
            "chunks/shared.js": abs["chunks/shared.js"]!,
          },
          runtime: "edge",
          filePath: abs["chunks/route.js"]!,
          edgeRuntime: {
            modulePath: abs["chunks/route.js"]!,
            entryKey: "middleware_app/api/edge/route",
            handlerExport: "handler",
          },
          config: { env: { __NEXT_BUILD_ID: "test-build" } },
          type: "APP_ROUTE",
        },
        {
          pathname: "/api/docs",
          id: "app/api/docs/route",
          sourcePage: "/api/docs/route",
          assets: {},
          runtime: "nodejs",
          filePath: nodeHandler,
          config: {},
          type: "APP_ROUTE",
        },
      ],
      middleware: {
        pathname: "/middleware",
        id: "middleware",
        sourcePage: "/middleware",
        assets: {
          "chunks/mw.js": abs["chunks/mw.js"]!,
          "chunks/shared.js": abs["chunks/shared.js"]!,
        },
        wasmAssets: { wasm_hello: wasmPath },
        runtime: "edge",
        filePath: abs["chunks/mw.js"]!,
        edgeRuntime: {
          modulePath: abs["chunks/mw.js"]!,
          entryKey: "middleware_middleware",
          handlerExport: "handler",
        },
        config: {
          env: { ...buildEnv, MIDDLEWARE_ONLY: "1" },
          matchers: [
            {
              source: "/dashboard/:path*",
              sourceRegex: "^/dashboard(?:/(.*))?$",
              has: [{ type: "header", key: "x-flag" }],
              missing: undefined,
            },
          ],
        },
        type: "MIDDLEWARE",
      },
      staticFiles: [],
      prerenders: [],
    },
    projectDir,
    repoRoot: projectDir,
    distDir: join(projectDir, ".next"),
    config: { basePath: "" },
    nextVersion: "16.2.10",
    buildId: "test-build",
  };

  vi.stubEnv("OCEL_OUTPUT_DIR", join(projectDir, ".ocel/output"));

  return { projectDir, args };
}

function outputDir(projectDir: string): string {
  return join(projectDir, ".ocel/output");
}

async function readBundleRaw(projectDir: string): Promise<string> {
  return readFile(join(outputDir(projectDir), "edge/bundle.json"), "utf8");
}

async function readBundle(projectDir: string) {
  return JSON.parse(await readBundleRaw(projectDir));
}

async function readManifest(projectDir: string) {
  return JSON.parse(
    await readFile(join(outputDir(projectDir), "routing-manifest.json"), "utf8"),
  );
}

async function exists(p: string): Promise<boolean> {
  try {
    await readFile(p);
    return true;
  } catch {
    return false;
  }
}

test("gives every edge entry key one bundle entry, variants folded together", async () => {
  const { projectDir, args } = await synthEdgeProject();

  await adapter.onBuildComplete!(args as never);

  const bundle = await readBundle(projectDir);
  expect(bundle.version).toBe(1);
  expect(bundle.mainModule).toBe("main.js");
  // /edge-page and /edge-page.rsc are two outputs but one compiled entry.
  expect(Object.keys(bundle.entries).sort()).toEqual([
    "middleware_app/api/edge/route",
    "middleware_app/edge-page/page",
    "middleware_middleware",
  ]);
  expect(bundle.entries["middleware_app/edge-page/page"].handlerExport).toBe(
    "handler",
  );
});

test("dedupes chunks by content and assigns ids in sorted-key order", async () => {
  const { projectDir, args } = await synthEdgeProject();

  await adapter.onBuildComplete!(args as never);

  const bundle = await readBundle(projectDir);
  // dup-a and dup-b hold identical bytes under different keys: one id.
  expect(bundle.chunks).toEqual({
    "c/0.js": "DUPLICATE",
    "c/1.js": "MW",
    "c/2.js": "PAGE",
    "c/3.js": "ROUTE",
    "c/4.js": "SHARED",
  });
  expect(bundle.entries["middleware_app/edge-page/page"].chunks).toEqual([
    "c/0.js",
    "c/2.js",
    "c/4.js",
  ]);
  expect(bundle.entries["middleware_app/api/edge/route"].chunks).toEqual([
    "c/0.js",
    "c/3.js",
    "c/4.js",
  ]);
  expect(bundle.entries["middleware_middleware"].chunks).toEqual([
    "c/1.js",
    "c/4.js",
  ]);
});

test("carries an entry's chunks in the order Next listed them", async () => {
  const { projectDir, args } = await synthEdgeProject();

  // Next lists a page's files in the order Turbopack must evaluate them, and a
  // chunk requiring a module from one that has not run yet dies on evaluation.
  // Ordered against the alphabet so re-sorting cannot pass by coincidence.
  const page = args.outputs.appPages[0]!;
  page.assets = {
    "chunks/shared.js": page.assets["chunks/shared.js"]!,
    "chunks/page.js": page.assets["chunks/page.js"]!,
    "chunks/dup-a.js": page.assets["chunks/dup-a.js"]!,
  };
  args.outputs.appPages[1]!.assets = page.assets;

  await adapter.onBuildComplete!(args as never);

  const bundle = await readBundle(projectDir);
  expect(bundle.entries["middleware_app/edge-page/page"].chunks).toEqual([
    "c/4.js",
    "c/2.js",
    "c/0.js",
  ]);
});

test("leaves source maps out of the bundle", async () => {
  const { projectDir, args } = await synthEdgeProject();

  await adapter.onBuildComplete!(args as never);

  const bundle = await readBundle(projectDir);
  expect(Object.values(bundle.chunks)).not.toContain('{"version":3}');
  expect(bundle.entries["middleware_app/edge-page/page"].chunks).toHaveLength(3);
});

test("carries wasm assets as base64, declared once for the whole bundle", async () => {
  const { projectDir, args } = await synthEdgeProject();

  await adapter.onBuildComplete!(args as never);

  const bundle = await readBundle(projectDir);
  expect(bundle.wasm).toEqual({
    "w/0.wasm": Buffer.from([0, 97, 115, 109]).toString("base64"),
  });
  // The worker declares every wasm module globally and workerd compiles the
  // unimported ones lazily, so no entry lists its own.
  for (const entry of Object.values(bundle.entries) as object[]) {
    expect(entry).not.toHaveProperty("wasm");
  }
});

test("writes byte-identical bundles for two builds with identical input", async () => {
  const first = await synthEdgeProject();
  await adapter.onBuildComplete!(first.args as never);
  const a = await readBundleRaw(first.projectDir);

  const second = await synthEdgeProject();
  await adapter.onBuildComplete!(second.args as never);
  const b = await readBundleRaw(second.projectDir);

  expect(b).toBe(a);
});

test("serializes the bundle with keys in sorted order at every level", async () => {
  const { projectDir, args } = await synthEdgeProject();

  await adapter.onBuildComplete!(args as never);

  const raw = await readBundleRaw(projectDir);
  const topLevel = Object.keys(JSON.parse(raw));
  expect(topLevel).toEqual([...topLevel].sort());
  const envKeys = Object.keys(JSON.parse(raw).env);
  expect(envKeys).toEqual([...envKeys].sort());
});

test("unions config.env across every edge output", async () => {
  const { projectDir, args } = await synthEdgeProject();

  await adapter.onBuildComplete!(args as never);

  const bundle = await readBundle(projectDir);
  expect(bundle.env).toEqual({
    __NEXT_BUILD_ID: "test-build",
    __NEXT_PREVIEW_MODE_ID: "preview",
    MIDDLEWARE_ONLY: "1",
  });
});

test("fails the build when two edge outputs disagree on an env value", async () => {
  const { args } = await synthEdgeProject();
  args.outputs.appRoutes[0]!.config.env = { __NEXT_BUILD_ID: "other-build" };

  await expect(adapter.onBuildComplete!(args as never)).rejects.toThrow(
    /__NEXT_BUILD_ID/,
  );
});

test("fails the build on nodejs middleware, naming the source file", async () => {
  const { projectDir, args } = await synthEdgeProject();
  args.outputs.middleware.runtime = "nodejs";

  await expect(adapter.onBuildComplete!(args as never)).rejects.toThrow(
    /\/middleware.*runtime: 'edge'/s,
  );
  expect(await exists(join(outputDir(projectDir), "edge/bundle.json"))).toBe(
    false,
  );
});

test("dispatches each edge pathname to its entry key", async () => {
  const { projectDir, args } = await synthEdgeProject();

  await adapter.onBuildComplete!(args as never);

  const manifest = await readManifest(projectDir);
  expect(manifest.dispatch["/edge-page"]).toEqual({
    kind: "edge",
    entryKey: "middleware_app/edge-page/page",
  });
  // The variant maps onto the same entry — no grouping, no symlink.
  expect(manifest.dispatch["/edge-page.rsc"]).toEqual({
    kind: "edge",
    entryKey: "middleware_app/edge-page/page",
  });
  expect(manifest.dispatch["/api/edge"]).toEqual({
    kind: "edge",
    entryKey: "middleware_app/api/edge/route",
  });
  // The nodejs route is untouched by any of this.
  expect(manifest.dispatch["/api/docs"]).toEqual({
    kind: "lambda",
    id: "app/api/docs/route",
  });
  expect(manifest.pathnames).toContain("/edge-page.rsc");
});

test("copies the middleware matchers into the routing manifest verbatim", async () => {
  const { projectDir, args } = await synthEdgeProject();

  await adapter.onBuildComplete!(args as never);

  const manifest = await readManifest(projectDir);
  expect(manifest.middleware.entryKey).toBe("middleware_middleware");
  expect(manifest.middleware.matchers).toEqual([
    {
      source: "/dashboard/:path*",
      sourceRegex: "^/dashboard(?:/(.*))?$",
      has: [{ type: "header", key: "x-flag" }],
    },
  ]);
});

test("records an empty matcher list for a middleware with no config", async () => {
  const { projectDir, args } = await synthEdgeProject();
  delete (args.outputs.middleware.config as { matchers?: unknown }).matchers;

  await adapter.onBuildComplete!(args as never);

  const manifest = await readManifest(projectDir);
  expect(manifest.middleware.matchers).toEqual([]);
});

test("emits no bundle and no middleware key for a build with neither", async () => {
  const { projectDir, args } = await synthEdgeProject();
  delete (args.outputs as { middleware?: unknown }).middleware;
  args.outputs.appPages = [];
  args.outputs.appRoutes = [args.outputs.appRoutes[1]!] as never;

  await adapter.onBuildComplete!(args as never);

  expect(await exists(join(outputDir(projectDir), "edge/bundle.json"))).toBe(
    false,
  );
  const manifest = await readManifest(projectDir);
  expect(manifest.middleware).toBeUndefined();
});

test("bundles middleware alone when the app has no edge routes", async () => {
  const { projectDir, args } = await synthEdgeProject();
  args.outputs.appPages = [];
  args.outputs.appRoutes = [args.outputs.appRoutes[1]!] as never;

  await adapter.onBuildComplete!(args as never);

  const bundle = await readBundle(projectDir);
  expect(Object.keys(bundle.entries)).toEqual(["middleware_middleware"]);
  expect(bundle.chunks).toEqual({ "c/0.js": "MW", "c/1.js": "SHARED" });
});

test("inlines the entries table into the shim and imports only a hit entry's chunks", async () => {
  const { projectDir, args } = await synthEdgeProject();

  await adapter.onBuildComplete!(args as never);

  const bundle = await readBundle(projectDir);
  expect(bundle.shim).toContain("middleware_middleware");
  expect(bundle.shim).toContain('await import("./" + id)');
  expect(bundle.shim).toContain("ctx.props.entryKey");
  // The shim reads env before the first chunk evaluates.
  expect(bundle.shim.indexOf("Object.assign(globalThis.process.env, env)")).
    toBeLessThan(bundle.shim.indexOf("await import"));
});

test("emits a shim that is a loadable module exporting a fetch handler", async () => {
  const { projectDir, args } = await synthEdgeProject();

  await adapter.onBuildComplete!(args as never);

  const { shim } = await readBundle(projectDir);
  const mod = await import(
    `data:text/javascript,${encodeURIComponent(shim)}`
  );
  expect(typeof mod.default.fetch).toBe("function");
});

// The bundled cache handler cannot read its binding from process.env: on the
// edge those are build-time string literals, and a binding is not a string.
test("hands the cache binding to the chunks on a global, before they evaluate", async () => {
  const { projectDir, args } = await synthEdgeProject();

  await adapter.onBuildComplete!(args as never);

  const { shim } = await readBundle(projectDir);
  expect(shim).toContain(
    "globalThis.__OCEL_EDGE_CACHE = { rpc: env.OCEL_CACHE_RPC, scope: env.OCEL_CACHE_SCOPE }",
  );
  expect(shim.indexOf("__OCEL_EDGE_CACHE")).toBeLessThan(
    shim.indexOf("await import"),
  );
});

// The dynamic worker's isolate is cached and long-lived, so a binding taken from
// a request's ctx is captured by whichever request cold-started it and disposed
// when that request ends — leaving requests 2..N holding a dead stub. Taking it
// from the load-time env, which carries the main worker's ctx.exports loopback,
// is what keeps a warm isolate working.
test("rebinds every request from the load-time env, never from a request's ctx", async () => {
  const { projectDir, args } = await synthEdgeProject();
  await adapter.onBuildComplete!(args as never);
  const { shim } = await readBundle(projectDir);
  const mod = await import(`data:text/javascript,${encodeURIComponent(shim)}`);

  const rpc = { fetchGet: () => null };
  const env = { OCEL_CACHE_RPC: rpc, OCEL_CACHE_SCOPE: "prod/app/build" };
  const seen: unknown[] = [];
  try {
    // An unknown entry key returns before any chunk is imported, which is what
    // makes the prelude drivable without a real Turbopack chunk on disk.
    for (let i = 0; i < 2; i++) {
      const ctx = {
        props: { entryKey: "unknown" },
        waitUntil: () => {},
        exports: { perRequest: i },
      };
      await mod.default.fetch(new Request("https://x/"), env, ctx);
      seen.push((globalThis as Record<string, any>).__OCEL_EDGE_CACHE);
    }
  } finally {
    delete (globalThis as Record<string, unknown>).__OCEL_EDGE_CACHE;
    delete process.env.OCEL_CACHE_RPC;
    delete process.env.OCEL_CACHE_SCOPE;
    for (const key of Object.keys(buildEnv)) delete process.env[key];
    delete process.env.MIDDLEWARE_ONLY;
  }

  for (const bound of seen) {
    expect(bound).toEqual({ rpc, scope: "prod/app/build" });
  }
});

test("warns that revalidate is inert for a prerender parented by an edge route", async () => {
  const { args } = await synthEdgeProject();
  const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
  args.outputs.prerenders = [
    {
      pathname: "/edge-page",
      id: "/edge-page",
      type: "PRERENDER",
      parentOutputId: "app/edge-page/page",
      groupId: 1,
      fallback: { filePath: undefined },
      config: {},
    },
  ] as never;

  await adapter.onBuildComplete!(args as never);

  expect(warn).toHaveBeenCalledWith(
    expect.stringContaining("revalidate is inert"),
  );
  expect(warn.mock.calls[0]![0]).toContain("/edge-page");
  expect(warn.mock.calls[0]![0]).toContain("ocelhq-b7l");
});

test("names the edge entry that regenerates a prerender its edge route parents", async () => {
  const { projectDir, args } = await synthEdgeProject();
  vi.spyOn(console, "warn").mockImplementation(() => {});
  args.outputs.prerenders = [
    {
      pathname: "/edge-page",
      id: "/edge-page",
      type: "PRERENDER",
      parentOutputId: "app/edge-page/page",
      groupId: 1,
      fallback: { filePath: undefined },
      config: {},
    },
    {
      pathname: "/api/docs",
      id: "/api/docs",
      type: "PRERENDER",
      parentOutputId: "app/api/docs/route",
      groupId: 2,
      fallback: { filePath: undefined },
      config: {},
    },
  ] as never;

  await adapter.onBuildComplete!(args as never);

  const manifest = await readManifest(projectDir);
  // The prerender entry replaces the plain edge one this pathname also
  // produced, so without the entry key the worker is left looking for a
  // Function URL an edge-rendered route never has.
  expect(manifest.dispatch["/edge-page"]).toMatchObject({
    kind: "prerender",
    entryKey: "middleware_app/edge-page/page",
  });
  // A prerender its Lambda regenerates carries none.
  expect(manifest.dispatch["/api/docs"]).not.toHaveProperty("entryKey");
});

test("prints the bundle size, chunk count and entry count", async () => {
  const { args } = await synthEdgeProject();
  const log = vi.spyOn(console, "log").mockImplementation(() => {});

  await adapter.onBuildComplete!(args as never);

  expect(log).toHaveBeenCalledWith(
    expect.stringMatching(/^ocel: edge bundle \d+\.\d MB, 5 chunks, 3 entries$/),
  );
});
