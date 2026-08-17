import { mkdtemp, mkdir, rm, writeFile, readFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { afterEach, expect, test, vi } from "vitest";

import adapter from "../src/next-adapter.mts";
import { defaultImages } from "./fixtures.mts";

afterEach(() => {
  vi.unstubAllEnvs();
  vi.restoreAllMocks();
});

const buildEnv = {
  __NEXT_BUILD_ID: "test-build",
  __NEXT_PREVIEW_MODE_ID: "preview",
};

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

  const tracedAssets = {
    "server/edge/assets/text.abc.txt": Buffer.from("PLAIN TEXT ASSET"),
    "server/edge/assets/pic.abc.png": Buffer.from([
      0x89, 0x50, 0x4e, 0x47, 0xff, 0xfe,
    ]),
    "server/edge/assets/worker.abc.js": Buffer.from("NOT A CHUNK"),
    "server/edge/assets/caf\u00e9.abc.txt": Buffer.from("ACCENTED"),
    "server/edge/assets/a`b${c}.abc.txt": Buffer.from("TEMPLATEY"),
  };
  const tracedAbs: Record<string, string> = {};
  for (const [name, bytes] of Object.entries(tracedAssets)) {
    tracedAbs[name] = join(projectDir, ".next", name);
    await mkdir(dirname(tracedAbs[name]!), { recursive: true });
    await writeFile(tracedAbs[name]!, bytes);
  }

  const middlewareManifest = join(
    projectDir,
    ".next/server/middleware-manifest.json",
  );
  await writeFile(
    middlewareManifest,
    JSON.stringify({
      version: 3,
      middleware: {
        "/": {
          name: "middleware",
          assets: [],
        },
      },
      functions: {
        "/edge-page": {
          name: "app/edge-page/page",
          assets: Object.keys(tracedAssets).map((name) => ({
            name,
            filePath: name,
          })),
        },
      },
    }),
  );

  const nodeHandler = join(projectDir, ".next/server/app/api/docs/route.js");
  await mkdir(dirname(nodeHandler), { recursive: true });
  await writeFile(nodeHandler, "module.exports = () => {}");

  const pageAssets = {
    "chunks/dup-a.js": abs["chunks/dup-a.js"]!,
    "chunks/page.js": abs["chunks/page.js"]!,
    "chunks/page.js.map": abs["chunks/page.js.map"]!,
    "chunks/shared.js": abs["chunks/shared.js"]!,
    ...tracedAbs,
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
    config: { basePath: "", images: defaultImages } as {
      basePath: string;
      images: unknown;
      deploymentId?: string;
    },
    nextVersion: "16.2.10",
    buildId: "test-build",
  };

  vi.stubEnv("OCEL_OUTPUT_DIR", join(projectDir, ".ocel/output"));

  return { projectDir, args, middlewareManifest, tracedAssets };
}

function outputDir(projectDir: string): string {
  return join(projectDir, ".ocel/output");
}

function assetTable(shim: string): Record<string, string> {
  const match = /^const ASSETS = (.*)$/m.exec(shim);
  if (!match) throw new Error("shim carries no ASSETS table");
  return JSON.parse(match[1]!) as Record<string, string>;
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
  expect(bundle.version).toBe(2);
  expect(bundle.mainModule).toBe("main.js");
  expect(Object.keys(bundle.entries).sort()).toEqual([
    "middleware_app/api/edge/route",
    "middleware_app/edge-page/page",
    "middleware_middleware",
  ]);
  expect(bundle.entries["middleware_app/edge-page/page"].handlerExport).toBe(
    "handler",
  );
});

test("gives the edge runtime the client asset suffix Next's sandbox would have set", async () => {
  const { projectDir, args } = await synthEdgeProject();
  args.config.deploymentId = "3f7c1b9a5e2d4c8f";

  await adapter.onBuildComplete!(args as never);

  const bundle = await readBundle(projectDir);
  expect(bundle.shim).toContain(
    'globalThis.NEXT_CLIENT_ASSET_SUFFIX = "?dpl=3f7c1b9a5e2d4c8f"',
  );
});

test("leaves the client asset suffix empty when no deployment id was stamped", async () => {
  const { projectDir, args } = await synthEdgeProject();

  await adapter.onBuildComplete!(args as never);

  expect((await readBundle(projectDir)).shim).toContain(
    'globalThis.NEXT_CLIENT_ASSET_SUFFIX = ""',
  );
});

test("dedupes chunks by content and assigns ids in sorted-key order", async () => {
  const { projectDir, args } = await synthEdgeProject();

  await adapter.onBuildComplete!(args as never);

  const bundle = await readBundle(projectDir);
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

test("keeps traced assets out of the chunk table", async () => {
  const { projectDir, args, tracedAssets } = await synthEdgeProject();

  await adapter.onBuildComplete!(args as never);

  const bundle = await readBundle(projectDir);
  const sources = Object.values(bundle.chunks) as string[];
  for (const bytes of Object.values(tracedAssets)) {
    expect(sources).not.toContain(bytes.toString("utf8"));
  }
  expect(bundle.entries["middleware_app/edge-page/page"].chunks).toHaveLength(3);
});

test("classifies by the middleware manifest, not by file extension", async () => {
  const { projectDir, args } = await synthEdgeProject();

  await adapter.onBuildComplete!(args as never);

  const bundle = await readBundle(projectDir);
  expect(Object.values(bundle.chunks)).not.toContain("NOT A CHUNK");
});

test("falls back to extensions when the manifest is unreadable", async () => {
  const { projectDir, args, middlewareManifest } = await synthEdgeProject();
  const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
  await rm(middlewareManifest);

  await adapter.onBuildComplete!(args as never);

  const bundle = await readBundle(projectDir);
  const sources = Object.values(bundle.chunks) as string[];
  expect(sources).not.toContain("PLAIN TEXT ASSET");
  expect(sources).not.toContain(
    Buffer.from([0x89, 0x50, 0x4e, 0x47, 0xff, 0xfe]).toString("utf8"),
  );
  expect(
    warn.mock.calls
      .map(([message]) => String(message))
      .some((message) => message.includes("middleware-manifest.json")),
  ).toBe(true);
});

test("carries traced assets as base64, byte-exact", async () => {
  const { projectDir, args, tracedAssets } = await synthEdgeProject();

  await adapter.onBuildComplete!(args as never);

  const bundle = await readBundle(projectDir);
  const ids = assetTable(bundle.shim);
  for (const [name, bytes] of Object.entries(tracedAssets)) {
    expect(Buffer.from(bundle.assets[ids[name]!], "base64")).toEqual(bytes);
  }
});

test("maps each asset's blob name to its module id in the shim", async () => {
  const { projectDir, args, tracedAssets } = await synthEdgeProject();

  await adapter.onBuildComplete!(args as never);

  const bundle = await readBundle(projectDir);
  const table = assetTable(bundle.shim);
  expect(Object.keys(table).sort()).toEqual(Object.keys(tracedAssets).sort());
  for (const id of Object.values(table)) {
    expect(bundle.assets).toHaveProperty(id);
  }
  expect(bundle.shim).toContain('url.startsWith("blob:")');
});

test("emits a loadable shim for an asset name carrying template syntax", async () => {
  const { projectDir, args } = await synthEdgeProject();

  await adapter.onBuildComplete!(args as never);

  const { shim } = await readBundle(projectDir);
  expect(assetTable(shim)).toHaveProperty("server/edge/assets/a`b${c}.abc.txt");
  const mod = await import(`data:text/javascript,${encodeURIComponent(shim)}`);
  expect(typeof mod.default.fetch).toBe("function");
});

test("keys the asset table by the build's name and decodes to reach it", async () => {
  const { projectDir, args } = await synthEdgeProject();

  await adapter.onBuildComplete!(args as never);

  const { shim } = await readBundle(projectDir);
  expect(assetTable(shim)).toHaveProperty("server/edge/assets/caf\u00e9.abc.txt");
  expect(shim).toContain("decodeURIComponent");
});

test("carries wasm assets as base64, declared once for the whole bundle", async () => {
  const { projectDir, args } = await synthEdgeProject();

  await adapter.onBuildComplete!(args as never);

  const bundle = await readBundle(projectDir);
  expect(bundle.wasm).toEqual({
    "w/0.wasm": Buffer.from([0, 97, 115, 109]).toString("base64"),
  });
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

test("excludes node middleware from the edge bundle", async () => {
  const { projectDir, args } = await synthEdgeProject();
  args.outputs.middleware.runtime = "nodejs";

  await adapter.onBuildComplete!(args as never);

  const bundle = await readBundle(projectDir);
  expect(Object.keys(bundle.entries)).not.toContain("middleware_middleware");
  expect(Object.values(bundle.chunks)).not.toContain("MW");
});

test("dispatches each edge pathname to its entry key", async () => {
  const { projectDir, args } = await synthEdgeProject();

  await adapter.onBuildComplete!(args as never);

  const manifest = await readManifest(projectDir);
  expect(manifest.dispatch["/edge-page"]).toEqual({
    kind: "edge",
    entryKey: "middleware_app/edge-page/page",
  });
  expect(manifest.dispatch["/edge-page.rsc"]).toEqual({
    kind: "edge",
    entryKey: "middleware_app/edge-page/page",
  });
  expect(manifest.dispatch["/api/edge"]).toEqual({
    kind: "edge",
    entryKey: "middleware_app/api/edge/route",
  });
  expect(manifest.dispatch["/api/docs"]).toEqual({
    kind: "lambda",
    id: "bundle-0",
    entryKey: "app/api/docs/route",
  });
  expect(manifest.pathnames).toContain("/edge-page.rsc");
});

test("copies the middleware matchers into the routing manifest verbatim", async () => {
  const { projectDir, args } = await synthEdgeProject();

  await adapter.onBuildComplete!(args as never);

  const manifest = await readManifest(projectDir);
  expect(manifest.middleware.runtime).toBe("edge");
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

test("rebinds every request from the load-time env, never from a request's ctx", async () => {
  const { projectDir, args } = await synthEdgeProject();
  await adapter.onBuildComplete!(args as never);
  const { shim } = await readBundle(projectDir);
  const mod = await import(`data:text/javascript,${encodeURIComponent(shim)}`);

  const rpc = { fetchGet: () => null };
  const env = { OCEL_CACHE_RPC: rpc, OCEL_CACHE_SCOPE: "prod/app/build" };
  const seen: unknown[] = [];
  try {
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
  expect(manifest.dispatch["/edge-page"]).toMatchObject({
    kind: "prerender",
    edgeEntryKey: "middleware_app/edge-page/page",
  });
  expect(manifest.dispatch["/edge-page"]).not.toHaveProperty("entryKey");
  expect(manifest.dispatch["/api/docs"]).not.toHaveProperty("edgeEntryKey");
  expect(manifest.dispatch["/api/docs"].entryKey).toBe("app/api/docs/route");
});

test("serves an edge Pages Router API route named by its index file at its directory", async () => {
  const { projectDir, args } = await synthEdgeProject();
  args.outputs.pagesApi.push({
    pathname: "/api/index",
    id: "pages/api/index",
    sourcePage: "api/index",
    assets: {},
    wasmAssets: {},
    runtime: "edge",
    filePath: join(projectDir, ".next/server/edge/chunks/route.js"),
    edgeRuntime: {
      modulePath: join(projectDir, ".next/server/edge/chunks/route.js"),
      entryKey: "middleware_pages/api/index",
      handlerExport: "handler",
    },
    config: { env: buildEnv },
    type: "PAGES_API",
  } as never);

  await adapter.onBuildComplete!(args as never);

  const manifest = await readManifest(projectDir);
  expect(manifest.dispatch["/api"]).toMatchObject({
    kind: "edge",
    entryKey: "middleware_pages/api/index",
  });
  expect(manifest.dispatch["/api/index"]).toBeUndefined();
  expect(manifest.pathnames).toContain("/api");
  expect(manifest.pathnames).not.toContain("/api/index");
});

test("binds each wasm module to the global name its chunks reach for", async () => {
  const { projectDir, args } = await synthEdgeProject();

  await adapter.onBuildComplete!(args as never);

  const bundle = await readBundle(projectDir);
  expect(bundle.shim).toContain('const WASM = {"wasm_hello":"w/0.wasm"}');
  expect(bundle.shim).toContain(
    'globalThis[name] ??= (await import("./" + id)).default',
  );
  expect(bundle.shim.indexOf("WASM")).toBeLessThan(
    bundle.shim.indexOf("for (const id of e.chunks)"),
  );
});

test("prints the bundle size, chunk count and entry count", async () => {
  const { args } = await synthEdgeProject();
  const log = vi.spyOn(console, "log").mockImplementation(() => {});

  await adapter.onBuildComplete!(args as never);

  expect(log).toHaveBeenCalledWith(
    expect.stringMatching(
      /^ocel: edge bundle \d+\.\d MB, 5 chunks, 5 assets, 3 entries$/,
    ),
  );
});

test("warns naming every edge route whose chunks carry ocel/env's edge build", async () => {
  const { projectDir, args } = await synthEdgeProject();
  const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
  await writeFile(
    join(projectDir, ".next/server/edge/chunks/route.js"),
    'class EnvEdgeError extends Error { name = "EnvEdgeError" }',
  );

  await adapter.onBuildComplete!(args as never);

  const call = warn.mock.calls
    .map(([message]) => String(message))
    .find((message) => message.includes("ocel/env"));
  expect(call).toBeDefined();
  expect(call).toContain("/api/edge");
  expect(call).not.toContain("/edge-page");
  expect(call).toContain("nodejs");
});

test("stays silent when no edge chunk carries ocel/env", async () => {
  const { args } = await synthEdgeProject();
  const warn = vi.spyOn(console, "warn").mockImplementation(() => {});

  await adapter.onBuildComplete!(args as never);

  for (const [message] of warn.mock.calls) {
    expect(String(message)).not.toContain("ocel/env");
  }
});

test("hands the running entry key to the chunks on a global, before they evaluate", async () => {
  const { projectDir, args } = await synthEdgeProject();

  await adapter.onBuildComplete!(args as never);

  const { shim } = await readBundle(projectDir);
  expect(shim).toContain("globalThis.__OCEL_EDGE_ENTRY = k");
  expect(shim.indexOf("__OCEL_EDGE_ENTRY")).toBeLessThan(
    shim.indexOf("await import"),
  );
});

test("writes no value of its own into the edge bundle's env", async () => {
  const { projectDir, args } = await synthEdgeProject();
  vi.stubEnv("OCEL_VAR_BAKED", "ciphertext-opened");
  vi.stubEnv("STRIPE_KEY", "sk-live-plaintext");

  await adapter.onBuildComplete!(args as never);

  const bundle = await readBundle(projectDir);
  const declared = new Set(
    [
      ...args.outputs.appPages,
      ...args.outputs.appRoutes,
      args.outputs.middleware,
    ].flatMap((o: any) => Object.keys(o?.config?.env ?? {})),
  );
  expect(Object.keys(bundle.env).every((k) => declared.has(k))).toBe(true);
  expect(JSON.stringify(bundle)).not.toContain("sk-live-plaintext");
  expect(JSON.stringify(bundle)).not.toContain("ciphertext-opened");
});
