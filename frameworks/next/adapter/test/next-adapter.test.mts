import {
  mkdtemp,
  mkdir,
  writeFile,
  readFile,
  readdir,
  stat,
  utimes,
} from "node:fs/promises";
import { createHash } from "node:crypto";
import { tmpdir } from "node:os";
import { dirname, join, relative } from "node:path";
import { fileURLToPath } from "node:url";
import {
  PHASE_DEVELOPMENT_SERVER,
  PHASE_PRODUCTION_BUILD,
} from "next/constants.js";
import { variantHeadersFile } from "@framework/next-cache/naming";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { defaultImages } from "./fixtures.mts";

let originalCwd: string;

beforeEach(() => {
  originalCwd = process.cwd();
});

afterEach(() => {
  process.chdir(originalCwd);
  vi.unstubAllEnvs();
});

async function loadAdapterIn(projectDir: string) {
  process.chdir(projectDir);
  vi.resetModules();
  const mod = await import("../src/next-adapter.mts");
  return mod.default;
}

async function synthProject() {
  const projectDir = await mkdtemp(join(tmpdir(), "ocel-next-"));

  await mkdir(join(projectDir, "public", "icons"), { recursive: true });
  await writeFile(join(projectDir, "public", "next.svg"), "<svg/>");
  await writeFile(join(projectDir, "public", "icons", "logo.png"), "x");

  const handler = join(
    projectDir,
    ".next/server/app/api/documents/route.js",
  );
  await mkdir(dirname(handler), { recursive: true });
  await writeFile(handler, "module.exports = () => {}");

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
      appPages: [],
      appRoutes: [
        {
          pathname: "/api/documents",
          id: "/api/documents",
          assets: {},
          runtime: "nodejs",
          filePath: handler,
          type: "APP_ROUTE",
        },
      ],
      staticFiles: [],
      prerenders: [],
    },
    projectDir,
    repoRoot: projectDir,
    distDir: join(projectDir, ".next"),
    config: { basePath: "", images: defaultImages },
    nextVersion: "16.2.10",
    buildId: "test-build",
  };

  return { projectDir, args };
}

async function exists(p: string): Promise<boolean> {
  try {
    await stat(p);
    return true;
  } catch {
    return false;
  }
}

async function allFileNames(dir: string): Promise<string[]> {
  try {
    const entries = await readdir(dir, { recursive: true, withFileTypes: true });
    return entries.filter((e) => e.isFile()).map((e) => e.name);
  } catch {
    return [];
  }
}

async function synthDedupProject() {
  const projectDir = await mkdtemp(join(tmpdir(), "ocel-next-dedup-"));

  const pageHandler = join(projectDir, ".next/server/app/page.js");
  const routeHandler = join(
    projectDir,
    ".next/server/app/api/documents/route.js",
  );
  const shared = join(projectDir, ".next/server/chunks/shared.js");
  for (const f of [pageHandler, routeHandler, shared]) {
    await mkdir(dirname(f), { recursive: true });
    await writeFile(f, "module.exports = () => {}");
  }
  const pageAssets = { "chunks/shared.js": shared };

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
          pathname: "/index.rsc",
          id: "/index.rsc",
          assets: pageAssets,
          runtime: "nodejs",
          filePath: pageHandler,
          config: {},
          type: "APP_PAGE",
        },
        {
          pathname: "/",
          id: "/",
          assets: pageAssets,
          runtime: "nodejs",
          filePath: pageHandler,
          config: {},
          type: "APP_PAGE",
        },
      ],
      appRoutes: [
        {
          pathname: "/api/documents",
          id: "/api/documents",
          assets: {},
          runtime: "nodejs",
          filePath: routeHandler,
          config: {},
          type: "APP_ROUTE",
        },
        {
          pathname: "/api/documents.rsc",
          id: "/api/documents.rsc",
          assets: {},
          runtime: "nodejs",
          filePath: routeHandler,
          config: {},
          type: "APP_ROUTE",
        },
      ],
      staticFiles: [],
      prerenders: [
        {
          pathname: "/",
          id: "/",
          parentOutputId: "/",
          fallback: { filePath: join(projectDir, "index.html") },
        },
        {
          pathname: "/index.rsc",
          id: "/index.rsc",
          parentOutputId: "/",
          fallback: { filePath: join(projectDir, "index.rsc") },
        },
      ],
    },
    projectDir,
    repoRoot: projectDir,
    distDir: join(projectDir, ".next"),
    config: { basePath: "", images: defaultImages },
    nextVersion: "16.2.10",
    buildId: "test-build",
  };

  return { projectDir, args };
}

async function synthPrerenderProject() {
  const projectDir = await mkdtemp(join(tmpdir(), "ocel-next-isr-"));

  const pageHandler = join(projectDir, ".next/server/app/page.js");
  await mkdir(dirname(pageHandler), { recursive: true });
  await writeFile(pageHandler, "module.exports = () => {}");

  await writeFile(
    join(projectDir, ".next/required-server-files.json"),
    JSON.stringify({
      version: 1,
      config: { cacheMaxMemorySize: 0, cacheHandlers: {} },
      appDir: projectDir,
      files: [],
      ignore: [],
    }),
  );

  const appDir = join(projectDir, ".next/server/app");
  await mkdir(join(appDir, "index.segments"), { recursive: true });
  await writeFile(join(appDir, "index.html"), "<html>root</html>");
  await writeFile(join(appDir, "index.rsc"), "RSC-ROOT");
  await writeFile(
    join(appDir, "index.segments/_tree.segment.rsc"),
    "RSC-TREE",
  );

  const richConfig = {
    allowQuery: [],
    allowHeader: ["host", "x-prerender-revalidate"],
    bypassFor: [{ type: "header", key: "next-action" }],
    bypassToken: "tok",
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
          pathname: "/index.rsc",
          id: "/index.rsc",
          assets: {},
          runtime: "nodejs",
          filePath: pageHandler,
          config: {},
          type: "APP_PAGE",
        },
        {
          pathname: "/",
          id: "/",
          assets: {},
          runtime: "nodejs",
          filePath: pageHandler,
          config: {},
          type: "APP_PAGE",
        },
      ],
      appRoutes: [],
      staticFiles: [],
      prerenders: [
        {
          pathname: "/",
          id: "/",
          type: "PRERENDER",
          parentOutputId: "/",
          groupId: 1,
          fallback: {
            filePath: join(appDir, "index.html"),
            initialRevalidate: false,
            initialHeaders: { "content-type": "text/html; charset=utf-8" },
          },
          config: richConfig,
        },
        {
          pathname: "/index.rsc",
          id: "/index.rsc",
          type: "PRERENDER",
          parentOutputId: "/",
          groupId: 1,
          fallback: {
            filePath: join(appDir, "index.rsc"),
            initialRevalidate: false,
            initialHeaders: { "content-type": "text/x-component" },
          },
          config: richConfig,
        },
        {
          pathname: "/index.segments/_tree.segment.rsc",
          id: "/index.segments/_tree.segment.rsc",
          type: "PRERENDER",
          parentOutputId: "/",
          groupId: 1,
          fallback: {
            filePath: join(appDir, "index.segments/_tree.segment.rsc"),
            initialRevalidate: false,
            initialHeaders: {
              "content-type": "text/x-component",
              "x-nextjs-postponed": "2",
              "x-nextjs-stale-time": "300",
            },
          },
          config: richConfig,
        },
      ],
    },
    projectDir,
    repoRoot: projectDir,
    distDir: join(projectDir, ".next"),
    config: { basePath: "", images: defaultImages },
    nextVersion: "16.2.10",
    buildId: "test-build",
  };

  return { projectDir, args };
}

function functionsDir(projectDir: string): string {
  return join(projectDir, ".ocel/output/functions");
}

async function partitionFuncDirs(projectDir: string) {
  const root = functionsDir(projectDir);
  const real: string[] = [];
  const links: string[] = [];
  const walk = async (dir: string) => {
    for (const entry of await readdir(dir, { withFileTypes: true })) {
      const abs = join(dir, entry.name);
      if (entry.isSymbolicLink() && entry.name.endsWith(".func")) {
        links.push(relative(root, abs));
      } else if (entry.isDirectory() && entry.name.endsWith(".func")) {
        real.push(relative(root, abs));
      } else if (entry.isDirectory()) {
        await walk(abs);
      }
    }
  };
  await walk(root);
  return { real: real.sort(), links: links.sort() };
}

async function readManifest(projectDir: string) {
  return JSON.parse(
    await readFile(
      join(projectDir, ".ocel/output/routing-manifest.json"),
      "utf8",
    ),
  );
}

async function readLauncher(projectDir: string, bundle = "bundle-0") {
  const source = await readFile(
    join(functionsDir(projectDir), `${bundle}.func/__next_launcher.cjs`),
    "utf8",
  );
  const table = (name: string) =>
    JSON.parse(source.match(new RegExp(`^const ${name} = (.*)$`, "m"))![1]!);
  return {
    source,
    entries: table("ENTRIES"),
    primary: table("PRIMARY"),
    routes: table("ROUTES"),
  };
}

async function expectManifestJoinsLaunchers(projectDir: string) {
  const entriesByBundle = new Map<string, Record<string, string>>();
  for (const name of await readdir(functionsDir(projectDir))) {
    if (!name.endsWith(".func")) continue;
    const bundle = name.slice(0, -".func".length);
    const { entries, primary } = await readLauncher(projectDir, bundle);
    entriesByBundle.set(bundle, entries);
    expect(Object.keys(entries)).toContain(primary);
  }
  expect(entriesByBundle.size).toBeGreaterThan(0);

  const dispatch = (await readManifest(projectDir)).dispatch as Record<
    string,
    { kind: string; id?: string; entryKey?: string; edgeEntryKey?: string }
  >;
  let joined = 0;
  for (const [pathname, target] of Object.entries(dispatch)) {
    if (target.kind !== "lambda" && target.kind !== "prerender") continue;
    if (target.kind === "prerender" && target.edgeEntryKey !== undefined) {
      expect(target.entryKey).toBeUndefined();
      continue;
    }
    const entries = entriesByBundle.get(target.id!);
    expect(entries, `${target.kind} ${pathname}: id ${target.id}`).toBeDefined();
    expect(
      Object.keys(entries!),
      `${target.kind} ${pathname}: entryKey ${target.entryKey}`,
    ).toContain(target.entryKey);
    joined += 1;
  }
  expect(joined).toBeGreaterThan(0);
}

test("packs every node route into one bundle .func", async () => {
  const { projectDir, args } = await synthDedupProject();
  const adapter = await loadAdapterIn(projectDir);

  await adapter.onBuildComplete(args as never);

  const { real, links } = await partitionFuncDirs(projectDir);
  expect(real).toEqual(["bundle-0.func"]);
  expect(links).toEqual([]);

  const bundle = join(functionsDir(projectDir), "bundle-0.func");
  for (const rel of [
    ".next/server/app/page.js",
    ".next/server/app/api/documents/route.js",
    "chunks/shared.js",
    "__next_launcher.cjs",
    "__ocel_dispatch.cjs",
  ]) {
    expect(await exists(join(bundle, rel))).toBe(true);
  }
});

test("copies the dispatcher into the bundle verbatim", async () => {
  const { projectDir, args } = await synthDedupProject();
  const adapter = await loadAdapterIn(projectDir);

  await adapter.onBuildComplete(args as never);

  expect(
    await readFile(
      join(functionsDir(projectDir), "bundle-0.func/__ocel_dispatch.cjs"),
      "utf8",
    ),
  ).toBe(
    await readFile(
      fileURLToPath(new URL("../src/next-dispatch.cjs", import.meta.url)),
      "utf8",
    ),
  );
});

test("declares every entry in the launcher with a POSIX relative specifier", async () => {
  const { projectDir, args } = await synthDedupProject();
  const adapter = await loadAdapterIn(projectDir);

  await adapter.onBuildComplete(args as never);

  const { entries, source } = await readLauncher(projectDir);
  expect(entries).toEqual({
    "/": "./.next/server/app/page.js",
    "/api/documents": "./.next/server/app/api/documents/route.js",
  });
  expect(source).toContain(`require("./__ocel_dispatch.cjs")`);
  expect(source).toContain("globalThis.AsyncLocalStorage = AsyncLocalStorage");
  expect(source).toContain("process.env.NODE_ENV ||= 'production'");
});

test("the launcher carries the pathname every route is served at", async () => {
  const { projectDir, args } = await synthDedupProject();
  const adapter = await loadAdapterIn(projectDir);

  await adapter.onBuildComplete(args as never);

  expect((await readLauncher(projectDir)).routes.exact).toEqual({
    "/": "/",
    "/index.rsc": "/",
    "/api/documents": "/api/documents",
    "/api/documents.rsc": "/api/documents",
  });
});

test("a basePath build's table is keyed by the prefixed pathnames Next hands over", async () => {
  const { projectDir, args } = await synthDedupProject();
  args.config = { ...args.config, basePath: "/docs" };
  for (const route of [...args.outputs.appPages, ...args.outputs.appRoutes]) {
    (route as { pathname: string }).pathname =
      `/docs${(route as { pathname: string }).pathname}`.replace(/\/$/, "") || "/";
  }
  for (const p of args.outputs.prerenders) {
    (p as { pathname: string }).pathname =
      `/docs${(p as { pathname: string }).pathname}`.replace(/\/$/, "") || "/";
  }
  const adapter = await loadAdapterIn(projectDir);

  await adapter.onBuildComplete(args as never);

  const { routes, entries } = await readLauncher(projectDir);
  expect(routes.exact).toEqual({
    "/docs": "/",
    "/docs/index.rsc": "/",
    "/docs/api/documents": "/api/documents",
    "/docs/api/documents.rsc": "/api/documents",
  });
  for (const key of Object.values(routes.exact)) expect(entries).toHaveProperty(key);
});

test("a dynamic route reaches the launcher as the pattern that spans it", async () => {
  const { projectDir, args } = await synthDedupProject();
  const handler = join(projectDir, ".next/server/app/api/todos/[id]/route.js");
  await mkdir(dirname(handler), { recursive: true });
  await writeFile(handler, "module.exports = () => {}");

  const dynamic = {
    assets: {},
    runtime: "nodejs",
    filePath: handler,
    type: "APP_ROUTE",
  };
  args.outputs.appRoutes.push(
    { pathname: "/api/todos/[id]", id: "/api/todos/[id]", ...dynamic } as never,
    {
      pathname: "/api/todos/[id].rsc",
      id: "/api/todos/[id].rsc",
      ...dynamic,
    } as never,
  );
  args.routing.dynamicRoutes.push(
    {
      source: "/api/todos/[id].rsc",
      sourceRegex:
        "^[/]?/api/todos/(?<nxtPid>[^/]+?)(?<rscSuffix>\\.rsc|\\.segments/.+\\.segment\\.rsc)(?:/)?$",
      destination: "/api/todos/[id]$rscSuffix?nxtPid=$nxtPid",
    } as never,
    {
      source: "/api/todos/[id]",
      sourceRegex: "^[/]?/api/todos/(?<nxtPid>[^/]+?)(?:/)?$",
      destination: "/api/todos/[id]?nxtPid=$nxtPid",
    } as never,
  );

  const adapter = await loadAdapterIn(projectDir);
  await adapter.onBuildComplete(args as never);

  const { routes } = await readLauncher(projectDir);
  expect(routes.dynamic).toEqual([
    ["^[/]?/api/todos/(?<nxtPid>[^/]+?)(?:/)?$", "/api/todos/[id]"],
  ]);
  expect(new RegExp(routes.dynamic[0][0], "i").test("/api/todos/7")).toBe(true);
});

test("primes the largest entry at INIT", async () => {
  const { projectDir, args } = await synthDedupProject();
  const adapter = await loadAdapterIn(projectDir);

  await adapter.onBuildComplete(args as never);

  expect((await readLauncher(projectDir)).primary).toBe("/");
});

test("elects the primary by traced bytes, not asset count", async () => {
  const { projectDir, args } = await synthDedupProject();
  const chunks = join(projectDir, ".next/server/chunks");
  const small: [string, string][] = [];
  for (const n of ["a", "b", "c"]) {
    const p = join(chunks, `small-${n}.js`);
    await writeFile(p, "x".repeat(64));
    small.push([`chunks/small-${n}.js`, p]);
  }
  const big = join(chunks, "big.js");
  await writeFile(big, "x".repeat(64 * 1024));

  args.outputs.appPages[1]!.assets = Object.fromEntries(small);
  args.outputs.appPages[0]!.assets = args.outputs.appPages[1]!.assets;
  args.outputs.appRoutes[0]!.assets = { "chunks/big.js": big };
  args.outputs.appRoutes[1]!.assets = args.outputs.appRoutes[0]!.assets;

  const adapter = await loadAdapterIn(projectDir);
  await adapter.onBuildComplete(args as never);

  const { entries, primary } = await readLauncher(projectDir);
  expect(Object.keys(entries)).toHaveLength(2);
  expect(primary).toBe("/api/documents");
});

test("joins every dispatch entry to its bundle's launcher table", async () => {
  const { projectDir, args } = await synthDedupProject();
  const adapter = await loadAdapterIn(projectDir);

  await adapter.onBuildComplete(args as never);

  await expectManifestJoinsLaunchers(projectDir);
});

test("fails the build when a route's entry lands in no bundle", async () => {
  const { projectDir, args } = await synthDedupProject();

  process.chdir(projectDir);
  vi.resetModules();
  vi.doMock("../src/pack.mts", async () => {
    const actual =
      await vi.importActual<typeof import("../src/pack.mts")>("../src/pack.mts");
    return {
      ...actual,
      packBundles: (members: never, opts: never) => {
        const result = actual.packBundles(members, opts as never);
        return {
          ...result,
          bundles: result.bundles.map((bundle) => ({
            ...bundle,
            members: bundle.members.filter(
              (m) => (m.member as { id: string }).id !== "/api/documents",
            ),
          })),
        };
      },
    };
  });
  try {
    const { default: adapter } = await import("../src/next-adapter.mts");
    await expect(adapter.onBuildComplete(args as never)).rejects.toThrow(
      /\/api\/documents/,
    );
  } finally {
    vi.doUnmock("../src/pack.mts");
    vi.resetModules();
  }
});

test("fails the build when a prerender's parent renders nowhere", async () => {
  const { projectDir, args } = await synthPrerenderProject();
  (args.outputs.prerenders[0] as Record<string, unknown>).parentOutputId =
    "/ghost";
  const adapter = await loadAdapterIn(projectDir);

  await expect(adapter.onBuildComplete(args as never)).rejects.toThrow(
    /\/ghost/,
  );
});

test("carries an empty-string entry key instead of dropping it", async () => {
  const { projectDir, args } = await synthPrerenderProject();
  args.outputs.appPages[1]!.id = "";
  for (const p of args.outputs.prerenders) p.parentOutputId = "";
  const adapter = await loadAdapterIn(projectDir);

  await adapter.onBuildComplete(args as never);

  const entry = (await readManifest(projectDir)).dispatch["/"];
  expect(entry.id).toBe("bundle-0");
  expect(entry.entryKey).toBe("");
  expect(Object.keys((await readLauncher(projectDir)).entries)).toEqual([""]);
});

test("warns once, aggregated, about traced assets with no source", async () => {
  const { projectDir, args } = await synthDedupProject();
  const ghosts = Object.fromEntries(
    ["one", "two"].map((n) => [
      `chunks/ghost-${n}.js`,
      join(projectDir, ".next/server/chunks", `ghost-${n}.js`),
    ]),
  );
  args.outputs.appRoutes[0]!.assets = ghosts;
  args.outputs.appRoutes[1]!.assets = ghosts;

  const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
  const warned: string[] = [];
  const adapter = await loadAdapterIn(projectDir);
  try {
    await adapter.onBuildComplete(args as never);
    warned.push(...warn.mock.calls.map((c) => String(c[0])));
  } finally {
    warn.mockRestore();
  }

  const lines = warned.filter((l) => l.includes("no source on disk"));
  expect(lines).toHaveLength(1);
  expect(lines[0]).toContain("not copied into the bundle");
  expect(lines[0]).toContain("2 traced asset(s)");
  expect(lines[0]).toContain("chunks/ghost-one.js");
  expect(lines[0]).toContain("chunks/ghost-two.js");
});

test("warns once about missing sources spanning several bundles", async () => {
  const { projectDir, args } = await synthDedupProject();
  const ghost = join(projectDir, ".next/server/chunks", "ghost.js");
  args.outputs.appRoutes[0]!.assets = { "chunks/ghost.js": ghost };
  args.outputs.appRoutes[1]!.assets = args.outputs.appRoutes[0]!.assets;
  args.outputs.appPages[0]!.assets = { "chunks/ghost.js": `${ghost}.other` };
  args.outputs.appPages[1]!.assets = args.outputs.appPages[0]!.assets;

  const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
  const warned: string[] = [];
  const adapter = await loadAdapterIn(projectDir);
  try {
    await adapter.onBuildComplete(args as never);
    warned.push(...warn.mock.calls.map((c) => String(c[0])));
  } finally {
    warn.mockRestore();
  }

  const { real } = await partitionFuncDirs(projectDir);
  expect(real).toEqual(["bundle-0.func", "bundle-1.func"]);
  const lines = warned.filter((l) => l.includes("no source on disk"));
  expect(lines).toHaveLength(1);
  expect(lines[0]).toContain("1 traced asset(s)");
});

test("elects the primary on a directory asset's recursive bytes", async () => {
  const { projectDir, args } = await synthDedupProject();
  const tree = join(projectDir, ".next/server/chunks/tree");
  await mkdir(join(tree, "nested"), { recursive: true });
  await writeFile(join(tree, "a.js"), "x".repeat(8 * 1024));
  await writeFile(join(tree, "nested", "b.js"), "x".repeat(8 * 1024));

  const lone = join(projectDir, ".next/server/chunks/lone.js");
  await writeFile(lone, "x".repeat(12 * 1024));

  args.outputs.appRoutes[0]!.assets = { "chunks/lone.js": lone };
  args.outputs.appRoutes[1]!.assets = args.outputs.appRoutes[0]!.assets;
  args.outputs.appPages[0]!.assets = { "chunks/tree": tree };
  args.outputs.appPages[1]!.assets = args.outputs.appPages[0]!.assets;

  const adapter = await loadAdapterIn(projectDir);
  await adapter.onBuildComplete(args as never);

  expect((await readLauncher(projectDir)).primary).toBe("/");
});

test("gives variants one shared bundle id and one shared entry key", async () => {
  const { projectDir, args } = await synthDedupProject();
  const adapter = await loadAdapterIn(projectDir);

  await adapter.onBuildComplete(args as never);

  const manifest = await readManifest(projectDir);
  expect(manifest.dispatch["/"]).toMatchObject({
    kind: "prerender",
    id: "bundle-0",
    entryKey: "/",
  });
  expect(manifest.dispatch["/index.rsc"]).toEqual(manifest.dispatch["/"]);
  expect(manifest.dispatch["/api/documents"]).toEqual({
    kind: "lambda",
    id: "bundle-0",
    entryKey: "/api/documents",
  });
  expect(manifest.dispatch["/api/documents.rsc"]).toEqual(
    manifest.dispatch["/api/documents"],
  );
  expect(manifest.pathnames).toContain("/index.rsc");
  expect(manifest.pathnames).toContain("/api/documents.rsc");
});

test("splits over the budget and points each route at its own bundle", async () => {
  const { projectDir, args } = await synthDedupProject();
  const filler = join(projectDir, ".next/server/chunks/filler.js");
  await writeFile(filler, "x".repeat(4096));
  (args.outputs.appRoutes[0] as Record<string, unknown>).assets = {
    "chunks/filler.js": filler,
  };
  args.outputs.appRoutes[1]!.assets = args.outputs.appRoutes[0]!.assets;
  const shared = args.outputs.appPages[0]!.assets["chunks/shared.js"]!;
  await writeFile(shared, "x".repeat(4096));

  process.chdir(projectDir);
  vi.resetModules();
  vi.doMock("../src/pack.mts", async () => {
    const actual =
      await vi.importActual<typeof import("../src/pack.mts")>("../src/pack.mts");
    return {
      ...actual,
      packBundles: (members: never, opts: never) =>
        actual.packBundles(members, { ...(opts as object), budgetBytes: 6000 }),
    };
  });
  try {
    const { default: adapter } = await import("../src/next-adapter.mts");
    await adapter.onBuildComplete(args as never);
  } finally {
    vi.doUnmock("../src/pack.mts");
    vi.resetModules();
  }

  const { real } = await partitionFuncDirs(projectDir);
  expect(real).toEqual(["bundle-0.func", "bundle-1.func"]);

  const manifest = await readManifest(projectDir);
  expect(manifest.dispatch["/"].id).toBe("bundle-0");
  expect(manifest.dispatch["/index.rsc"].id).toBe("bundle-0");
  expect(manifest.dispatch["/api/documents"].id).toBe("bundle-1");
  expect(manifest.dispatch["/api/documents.rsc"].id).toBe("bundle-1");

  expect(Object.keys((await readLauncher(projectDir, "bundle-0")).entries)).toEqual(["/"]);
  expect(
    Object.keys((await readLauncher(projectDir, "bundle-1")).entries),
  ).toEqual(["/api/documents"]);

  const whole = {
    "/": "/",
    "/index.rsc": "/",
    "/api/documents": "/api/documents",
    "/api/documents.rsc": "/api/documents",
  };
  expect((await readLauncher(projectDir, "bundle-0")).routes.exact).toEqual(whole);
  expect((await readLauncher(projectDir, "bundle-1")).routes.exact).toEqual(whole);
});

test("copies public/ files into the static output, recursively", async () => {
  const { projectDir, args } = await synthProject();
  const adapter = await loadAdapterIn(projectDir);

  await adapter.onBuildComplete(args as never);

  const staticDir = join(projectDir, ".ocel/output/static");
  expect(await exists(join(staticDir, "next.svg"))).toBe(true);
  expect(await exists(join(staticDir, "icons/logo.png"))).toBe(true);
});

test("enumerates public/ files as static in the routing manifest", async () => {
  const { projectDir, args } = await synthProject();
  const adapter = await loadAdapterIn(projectDir);

  await adapter.onBuildComplete(args as never);

  const manifest = JSON.parse(
    await readFile(
      join(projectDir, ".ocel/output/routing-manifest.json"),
      "utf8",
    ),
  );

  expect(manifest.pathnames).toContain("/next.svg");
  expect(manifest.pathnames).toContain("/icons/logo.png");
  expect(manifest.dispatch["/next.svg"]).toEqual({ kind: "static" });
  expect(manifest.dispatch["/icons/logo.png"]).toEqual({ kind: "static" });
});

interface StaticOutputs {
  outputs: { staticFiles: { pathname: string; id: string; filePath: string }[] };
}

async function addStaticOutput(
  args: StaticOutputs,
  pathname: string,
  filePath: string,
  contents: string,
) {
  await mkdir(dirname(filePath), { recursive: true });
  await writeFile(filePath, contents);
  args.outputs.staticFiles.push({ pathname, id: pathname, filePath });
}

async function withStaticFile(
  projectDir: string,
  args: StaticOutputs,
  pathname: string,
  contents: string,
) {
  await addStaticOutput(args, pathname, join(projectDir, ".next", pathname), contents);
}

async function withStaticPage(
  projectDir: string,
  args: StaticOutputs,
  pathname: string,
  contents: string,
) {
  const filePath = join(projectDir, ".next/server/pages", `${pathname}.html`);
  await addStaticOutput(args, pathname, filePath, contents);
}

test("carries the compiled image config and its hash into the manifest", async () => {
  const { projectDir, args } = await synthProject();
  args.config.images = {
    ...defaultImages,
    remotePatterns: [{ protocol: "https", hostname: "*.example.com" }],
  };
  const adapter = await loadAdapterIn(projectDir);

  await adapter.onBuildComplete(args as never);

  const { images } = await readManifest(projectDir);
  expect(images.path).toBe("/_next/image");
  expect(images.minimumCacheTTL).toBe(14400);
  expect(images.remotePatterns[0].protocol).toBe("https");
  expect(new RegExp(images.remotePatterns[0].hostname).test("a.example.com")).toBe(
    true,
  );
  expect(images.configHash).toMatch(/^[0-9a-f]{64}$/);
});

test("writes an image config artifact the manifest's configHash covers", async () => {
  const { projectDir, args } = await synthProject();
  const adapter = await loadAdapterIn(projectDir);

  await adapter.onBuildComplete(args as never);

  const bytes = await readFile(
    join(projectDir, ".ocel/output/image-config.json"),
  );
  const { images } = await readManifest(projectDir);
  expect(createHash("sha256").update(bytes).digest("hex")).toBe(
    images.configHash,
  );
  expect(JSON.parse(bytes.toString()).configHash).toBeUndefined();
});

test("hashes both public/ and built static files into the manifest", async () => {
  const { projectDir, args } = await synthProject();
  await withStaticFile(projectDir, args, "/_next/static/media/logo.png", "PNG");
  const adapter = await loadAdapterIn(projectDir);

  await adapter.onBuildComplete(args as never);

  const { assetHashes } = await readManifest(projectDir);
  expect(assetHashes["/_next/static/media/logo.png"]).toBe(
    createHash("sha256").update("PNG").digest("hex"),
  );
  expect(assetHashes["/icons/logo.png"]).toBe(
    createHash("sha256").update("x").digest("hex"),
  );
  expect(assetHashes["/next.svg"]).toBe(
    createHash("sha256").update("<svg/>").digest("hex"),
  );
  expect(
    await readFile(
      join(projectDir, ".ocel/output/static/_next/static/media/logo.png"),
      "utf8",
    ),
  ).toBe("PNG");
});

test("keys the error pages by the path they are served at", async () => {
  const { projectDir, args } = await synthProject();
  await withStaticPage(projectDir, args, "/404", "gone");
  const adapter = await loadAdapterIn(projectDir);

  await adapter.onBuildComplete(args as never);

  const { assetHashes } = await readManifest(projectDir);
  expect(assetHashes["/404.html"]).toBe(
    createHash("sha256").update("gone").digest("hex"),
  );
  expect(assetHashes["/404"]).toBeUndefined();
});

test("writes a statically-optimized page under its .html name", async () => {
  const { projectDir, args } = await synthProject();
  await withStaticPage(projectDir, args, "/some", "<html>some</html>");
  const adapter = await loadAdapterIn(projectDir);

  await adapter.onBuildComplete(args as never);

  const staticDir = join(projectDir, ".ocel/output/static");
  expect(await readFile(join(staticDir, "some.html"), "utf8")).toBe(
    "<html>some</html>",
  );
  expect(await exists(join(staticDir, "some"))).toBe(false);

  const manifest = await readManifest(projectDir);
  expect(manifest.assetHashes["/some.html"]).toBe(
    createHash("sha256").update("<html>some</html>").digest("hex"),
  );
  expect(manifest.dispatch["/some"]).toEqual({ kind: "static" });
  expect(manifest.dispatch["/some.html"]).toBeUndefined();
  expect(manifest.pathnames).toContain("/some");
});

test("gives a data-fetch-less page no _next/data pathname when the app has no middleware", async () => {
  const { projectDir, args } = await synthProject();
  await withStaticPage(projectDir, args, "/some", "<html>some</html>");
  const adapter = await loadAdapterIn(projectDir);

  await adapter.onBuildComplete(args as never);

  const manifest = await readManifest(projectDir);
  const dataPathname = `/_next/data/${args.buildId}/some.json`;
  expect(manifest.pathnames).not.toContain(dataPathname);
  expect(manifest.dispatch[dataPathname]).toBeUndefined();
});

test("gives a data-fetch-less page a _next/data pathname and dispatch entry when the app has middleware", async () => {
  const { projectDir, args } = await synthProject();
  await withStaticPage(projectDir, args, "/some", "<html>some</html>");
  await withNodeMiddleware(projectDir, args);
  const adapter = await loadAdapterIn(projectDir);

  await adapter.onBuildComplete(args as never);

  const manifest = await readManifest(projectDir);
  const dataPathname = `/_next/data/${args.buildId}/some.json`;
  expect(manifest.pathnames).toContain(dataPathname);
  expect(manifest.dispatch[dataPathname]).toEqual(manifest.dispatch["/some"]);
});

test("emits a page and its own children without colliding", async () => {
  const { projectDir, args } = await synthProject();
  await withStaticPage(projectDir, args, "/overlap", "<html>parent</html>");
  await withStaticFile(projectDir, args, "/overlap.rsc", "RSC");
  await withStaticPage(projectDir, args, "/overlap/[slug]", "<html>child</html>");
  await withStaticFile(projectDir, args, "/overlap/[slug].rsc", "CHILD RSC");
  const adapter = await loadAdapterIn(projectDir);

  await adapter.onBuildComplete(args as never);

  const staticDir = join(projectDir, ".ocel/output/static");
  expect(await readFile(join(staticDir, "overlap.html"), "utf8")).toBe(
    "<html>parent</html>",
  );
  expect(await readFile(join(staticDir, "overlap/[slug].html"), "utf8")).toBe(
    "<html>child</html>",
  );
  expect(await readFile(join(staticDir, "overlap.rsc"), "utf8")).toBe("RSC");
  expect(await readFile(join(staticDir, "overlap/[slug].rsc"), "utf8")).toBe(
    "CHILD RSC",
  );
});

test("writes a statically-optimized dynamic page under its template's name", async () => {
  const { projectDir, args } = await synthProject();
  args.config = { ...args.config, basePath: "/docs" };
  await withStaticPage(projectDir, args, "/docs/[slug]", "<html>slug</html>");
  const adapter = await loadAdapterIn(projectDir);

  await adapter.onBuildComplete(args as never);

  const staticDir = join(projectDir, ".ocel/output/static");
  expect(await readFile(join(staticDir, "docs/[slug].html"), "utf8")).toBe(
    "<html>slug</html>",
  );

  const manifest = await readManifest(projectDir);
  expect(manifest.assetHashes["/docs/[slug].html"]).toBe(
    createHash("sha256").update("<html>slug</html>").digest("hex"),
  );
  expect(manifest.dispatch["/docs/[slug]"]).toEqual({ kind: "static" });
  expect(manifest.pathnames).toContain("/docs/[slug]");
});

test("leaves static outputs that are already files alone", async () => {
  const { projectDir, args } = await synthProject();
  await withStaticFile(projectDir, args, "/_next/static/chunks/a.js", "JS");
  await withStaticFile(projectDir, args, "/favicon.ico", "ICO");
  await withStaticFile(projectDir, args, "/opengraph-image.png", "PNG");
  await withStaticFile(projectDir, args, "/sitemap.xml", "<urlset/>");
  const adapter = await loadAdapterIn(projectDir);

  await adapter.onBuildComplete(args as never);

  const staticDir = join(projectDir, ".ocel/output/static");
  expect(await exists(join(staticDir, "_next/static/chunks/a.js"))).toBe(true);
  expect(await exists(join(staticDir, "favicon.ico"))).toBe(true);
  expect(await exists(join(staticDir, "opengraph-image.png"))).toBe(true);
  expect(await readFile(join(staticDir, "sitemap.xml"), "utf8")).toBe("<urlset/>");
  expect(await exists(join(staticDir, "_next/static/chunks/a.js.html"))).toBe(
    false,
  );
  expect(await exists(join(staticDir, "favicon.ico.html"))).toBe(false);
  expect(await exists(join(staticDir, "sitemap.xml.html"))).toBe(false);
});

test("emits a dotted page and its own children without colliding", async () => {
  const { projectDir, args } = await synthProject();
  await withStaticPage(projectDir, args, "/v1.0", "<html>parent</html>");
  await withStaticPage(projectDir, args, "/v1.0/[slug]", "<html>child</html>");
  const adapter = await loadAdapterIn(projectDir);

  await adapter.onBuildComplete(args as never);

  const staticDir = join(projectDir, ".ocel/output/static");
  expect(await readFile(join(staticDir, "v1.0.html"), "utf8")).toBe(
    "<html>parent</html>",
  );
  expect(await readFile(join(staticDir, "v1.0/[slug].html"), "utf8")).toBe(
    "<html>child</html>",
  );
  expect((await readManifest(projectDir)).dispatch["/v1.0"]).toEqual({
    kind: "static",
  });
});

test("copies an extensionless public/ file under its own name", async () => {
  const { projectDir, args } = await synthProject();
  await writeFile(join(projectDir, "public", "LICENSE"), "MIT");
  const adapter = await loadAdapterIn(projectDir);

  await adapter.onBuildComplete(args as never);

  const staticDir = join(projectDir, ".ocel/output/static");
  expect(await readFile(join(staticDir, "LICENSE"), "utf8")).toBe("MIT");
  expect(await exists(join(staticDir, "LICENSE.html"))).toBe(false);
  expect((await readManifest(projectDir)).assetHashes["/LICENSE"]).toBe(
    createHash("sha256").update("MIT").digest("hex"),
  );
});

test("omits the image config when the app opted out of optimization", async () => {
  const { projectDir, args } = await synthProject();
  args.config.images = { ...defaultImages, unoptimized: true };
  const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
  const adapter = await loadAdapterIn(projectDir);

  await adapter.onBuildComplete(args as never);

  expect((await readManifest(projectDir)).images).toBeUndefined();
  expect(await exists(join(projectDir, ".ocel/output/image-config.json"))).toBe(
    false,
  );
  expect(warn).toHaveBeenCalledWith(
    expect.stringMatching(/images\.unoptimized is true/),
  );
  warn.mockRestore();
});

test("records the x-vercel-cache opt-in when the deploying process set the flag", async () => {
  const { projectDir, args } = await synthProject();
  vi.stubEnv("OCEL_E2E_VERCEL_CACHE_HEADER", "1");
  const adapter = await loadAdapterIn(projectDir);

  await adapter.onBuildComplete(args as never);

  expect((await readManifest(projectDir)).vercelCacheAlias).toBe(true);
});

test("omits the x-vercel-cache opt-in from an ordinary build", async () => {
  const { projectDir, args } = await synthProject();
  const adapter = await loadAdapterIn(projectDir);

  await adapter.onBuildComplete(args as never);

  expect(await readManifest(projectDir)).not.toHaveProperty("vercelCacheAlias");
});

test("carries the app's trailing-slash config into the routing manifest", async () => {
  const { projectDir, args } = await synthProject();
  args.config = {
    ...args.config,
    trailingSlash: true,
    skipTrailingSlashRedirect: true,
    skipMiddlewareUrlNormalize: true,
  };
  const adapter = await loadAdapterIn(projectDir);

  await adapter.onBuildComplete(args as never);

  const manifest = await readManifest(projectDir);
  expect(manifest.trailingSlash).toBe(true);
  expect(manifest.skipTrailingSlashRedirect).toBe(true);
  expect(manifest.skipMiddlewareUrlNormalize).toBe(true);
});

test("defaults the trailing-slash config to false when the app sets none of it", async () => {
  const { projectDir, args } = await synthProject();
  const adapter = await loadAdapterIn(projectDir);

  await adapter.onBuildComplete(args as never);

  const manifest = await readManifest(projectDir);
  expect(manifest.trailingSlash).toBe(false);
  expect(manifest.skipTrailingSlashRedirect).toBe(false);
  expect(manifest.skipMiddlewareUrlNormalize).toBe(false);
});

test("writes no Vercel-style prerender config or fallback files", async () => {
  const { projectDir, args } = await synthPrerenderProject();
  const adapter = await loadAdapterIn(projectDir);

  await adapter.onBuildComplete(args as never);

  const names = await allFileNames(functionsDir(projectDir));
  expect(names.some((n) => n.endsWith(".prerender-config.json"))).toBe(false);
  expect(names.some((n) => n.includes(".prerender-fallback."))).toBe(false);
});

test("marks prerendered pathnames as prerender in dispatch", async () => {
  const { projectDir, args } = await synthPrerenderProject();
  const adapter = await loadAdapterIn(projectDir);

  await adapter.onBuildComplete(args as never);

  const manifest = await readManifest(projectDir);
  expect(manifest.dispatch["/"]).toMatchObject({
    kind: "prerender",
    id: "bundle-0",
    entryKey: "/",
  });
  expect(manifest.dispatch["/index.rsc"]).toMatchObject({
    kind: "prerender",
    id: "bundle-0",
    entryKey: "/",
  });
  expect(manifest.dispatch["/index.segments/_tree.segment.rsc"]).toMatchObject({
    kind: "prerender",
    id: "bundle-0",
    entryKey: "/",
  });
});

test("separates a node prerender's entryKey from an edge prerender's edgeEntryKey", async () => {
  const { projectDir, args } = await synthPrerenderProject();
  const edgePage = join(projectDir, ".next/server/app/edgy/page.js");
  await mkdir(dirname(edgePage), { recursive: true });
  await writeFile(edgePage, "module.exports = () => {}");
  args.outputs.appPages.push({
    pathname: "/edgy",
    id: "/edgy",
    assets: {},
    runtime: "edge",
    filePath: edgePage,
    config: {},
    type: "APP_PAGE",
    edgeRuntime: { entryKey: "app/edgy/page", handlerExport: "default" },
  } as never);
  args.outputs.prerenders.push({
    pathname: "/edgy",
    id: "/edgy",
    type: "PRERENDER",
    parentOutputId: "/edgy",
    groupId: 9,
    fallback: { filePath: join(projectDir, ".next/server/app/edgy.html") },
    config: {},
  } as never);

  const adapter = await loadAdapterIn(projectDir);
  await adapter.onBuildComplete(args as never);

  const manifest = await readManifest(projectDir);
  expect(manifest.dispatch["/"].entryKey).toBe("/");
  expect(manifest.dispatch["/"].edgeEntryKey).toBeUndefined();
  expect(manifest.dispatch["/edgy"].edgeEntryKey).toBe("app/edgy/page");
  expect(manifest.dispatch["/edgy"].entryKey).toBeUndefined();
  await expectManifestJoinsLaunchers(projectDir);
});

test("lists every prerender pathname (including .segment.rsc) so resolveRoutes can match it", async () => {
  const { projectDir, args } = await synthPrerenderProject();
  const adapter = await loadAdapterIn(projectDir);

  await adapter.onBuildComplete(args as never);

  const manifest = await readManifest(projectDir);
  expect(manifest.pathnames).toContain("/index.segments/_tree.segment.rsc");
  for (const p of args.outputs.prerenders) {
    expect(manifest.pathnames).toContain(p.pathname);
  }
});

test("projects a prerender's fallback down to its freshness windows and pprChain", async () => {
  const { projectDir, args } = await synthPrerenderProject();
  const root = args.outputs.prerenders[0] as Record<string, any>;
  root.pprChain = { headers: { "next-resume": "1" } };
  root.fallback.initialRevalidate = 3600;
  root.fallback.initialExpiration = 86400;
  root.fallback.initialStatus = 200;
  root.fallback.postponedState = "2867:[1,{}]";

  const adapter = await loadAdapterIn(projectDir);
  await adapter.onBuildComplete(args as never);

  const entry = (await readManifest(projectDir)).dispatch["/"];
  expect(entry.fallback).toEqual({
    initialRevalidate: 3600,
    initialExpiration: 86400,
  });
  expect(entry.pprChain).toEqual({ headers: { "next-resume": "1" } });
});

test("emits no build-machine file paths in the routing manifest", async () => {
  const { projectDir, args } = await synthPrerenderProject();
  const adapter = await loadAdapterIn(projectDir);

  await adapter.onBuildComplete(args as never);

  const manifest = JSON.stringify(await readManifest(projectDir));
  expect(manifest).not.toContain(projectDir);
});

test("records the ocel app name (from OCEL_APP_NAME) in the routing manifest", async () => {
  const { projectDir, args } = await synthProject();
  const adapter = await loadAdapterIn(projectDir);

  process.env.OCEL_APP_NAME = "marketing";
  try {
    await adapter.onBuildComplete(args as never);
  } finally {
    delete process.env.OCEL_APP_NAME;
  }

  const manifest = await readManifest(projectDir);
  expect(manifest.appName).toBe("marketing");
});

test("writes the bundle name into its config.json", async () => {
  const { projectDir, args } = await synthProject();
  const adapter = await loadAdapterIn(projectDir);

  await adapter.onBuildComplete(args as never);

  const config = JSON.parse(
    await readFile(
      join(projectDir, ".ocel/output/functions/bundle-0.func/config.json"),
      "utf8",
    ),
  );

  expect(config.id).toBe("bundle-0");
  expect(config.handler).toBe("__next_launcher.cjs");
  expect(config.framework).toBe("next");
});

test("records the owning app in each function's config.json", async () => {
  const { projectDir, args } = await synthProject();
  const adapter = await loadAdapterIn(projectDir);

  process.env.OCEL_APP_NAME = "marketing";
  try {
    await adapter.onBuildComplete(args as never);
  } finally {
    delete process.env.OCEL_APP_NAME;
  }

  const config = JSON.parse(
    await readFile(
      join(projectDir, ".ocel/output/functions/bundle-0.func/config.json"),
      "utf8",
    ),
  );

  expect(config.app).toBe("marketing");
});

async function readCacheEntry(projectDir: string, key: string) {
  return JSON.parse(
    await readFile(
      join(projectDir, ".ocel/output/cache", `${key}.cache.json`),
      "utf8",
    ),
  );
}

test("copies the cache handler into the app tree and names it there", async () => {
  const projectDir = await mkdtemp(join(tmpdir(), "ocel-next-cfg-"));
  const adapter = await loadAdapterIn(projectDir);

  const config = await adapter.modifyConfig!({} as never, {
    phase: PHASE_PRODUCTION_BUILD,
    nextVersion: "16.2.10",
  });

  const dest = join(projectDir, ".ocel/cache-handler.cjs");
  expect(config.cacheHandler).toBe(dest);
  expect(await readFile(dest, "utf8")).toBe(
    await readFile(
      fileURLToPath(new URL("../src/edge-cache-handler.cjs", import.meta.url)),
      "utf8",
    ),
  );
});

test("names the singular handler only, never the 'use cache' map", async () => {
  const projectDir = await mkdtemp(join(tmpdir(), "ocel-next-cfg-"));
  const adapter = await loadAdapterIn(projectDir);

  const config = await adapter.modifyConfig!({ cacheMaxMemorySize: 1 } as never, {
    phase: PHASE_PRODUCTION_BUILD,
    nextVersion: "16.2.10",
  });

  expect(config.cacheHandlers).toBeUndefined();
  expect(config.cacheMaxMemorySize).toBe(0);
});

test("leaves a non-build phase untouched and writes nothing", async () => {
  const projectDir = await mkdtemp(join(tmpdir(), "ocel-next-cfg-"));
  const adapter = await loadAdapterIn(projectDir);

  const config = await adapter.modifyConfig!({ cacheMaxMemorySize: 1 } as never, {
    phase: PHASE_DEVELOPMENT_SERVER,
    nextVersion: "16.2.10",
  });

  expect(config.cacheHandler).toBeUndefined();
  expect(config.cacheMaxMemorySize).toBe(1);
  await expect(
    readFile(join(projectDir, ".ocel/cache-handler.cjs"), "utf8"),
  ).rejects.toThrow();
});

test("names the layer's cache handler by absolute path in required-server-files", async () => {
  const { projectDir, args } = await synthPrerenderProject();
  const adapter = await loadAdapterIn(projectDir);

  await adapter.onBuildComplete(args as never);

  const manifest = JSON.parse(
    await readFile(join(projectDir, ".next/required-server-files.json"), "utf8"),
  );
  expect(manifest.config.cacheHandler).toBe("/opt/ocel/next/cache-handler.cjs");
  expect(manifest.config.cacheMaxMemorySize).toBe(0);
  expect(manifest.version).toBe(1);
});

test("registers the 'use cache' handlers by absolute path alongside the ISR one", async () => {
  const { projectDir, args } = await synthPrerenderProject();
  const adapter = await loadAdapterIn(projectDir);

  await adapter.onBuildComplete(args as never);

  const manifest = JSON.parse(
    await readFile(join(projectDir, ".next/required-server-files.json"), "utf8"),
  );
  expect(manifest.config.cacheHandlers).toEqual({
    default: "/opt/ocel/next/use-cache-default.cjs",
    remote: "/opt/ocel/next/use-cache-remote.cjs",
  });
  expect(manifest.config.cacheHandler).toBe("/opt/ocel/next/cache-handler.cjs");
});

test("regroups a route's prerender outputs into one cache entry", async () => {
  const { projectDir, args } = await synthPrerenderProject();
  const adapter = await loadAdapterIn(projectDir);

  await adapter.onBuildComplete(args as never);

  const entry = await readCacheEntry(projectDir, "index");

  expect(entry.value.kind).toBe("APP_PAGE");
  expect(entry.value.html).toBe("<html>root</html>");
  expect(Buffer.from(entry.value.rscData, "base64").toString()).toBe("RSC-ROOT");
  expect(entry.value.segmentData).toEqual({
    "/_tree": Buffer.from("RSC-TREE").toString("base64"),
  });
  expect(typeof entry.lastModified).toBe("number");
});

test("carries a pages route's data twin onto its cache entry", async () => {
  const { projectDir, args } = await synthPrerenderProject();
  const pagesDir = join(projectDir, ".next/server/pages");
  await mkdir(pagesDir, { recursive: true });
  const handler = join(pagesDir, "blog.js");
  await writeFile(handler, "module.exports = () => {}");
  await writeFile(join(pagesDir, "blog.html"), "<html>blog</html>");
  await writeFile(
    join(pagesDir, "blog.json"),
    JSON.stringify({ pageProps: { title: "blog" } }),
  );

  args.outputs.pages.push({
    pathname: "/blog",
    id: "/blog",
    assets: {},
    runtime: "nodejs",
    filePath: handler,
    config: {},
    type: "PAGES",
  } as never);
  args.outputs.prerenders.push(
    {
      pathname: "/blog",
      id: "/blog",
      type: "PRERENDER",
      parentOutputId: "/blog",
      groupId: 2,
      fallback: {
        filePath: join(pagesDir, "blog.html"),
        initialRevalidate: false,
        initialHeaders: { "content-type": "text/html; charset=utf-8" },
      },
      config: { allowQuery: [] },
    } as never,
    {
      pathname: "/_next/data/test-build/blog.json",
      id: "/_next/data/test-build/blog.json",
      type: "PRERENDER",
      parentOutputId: "/blog",
      groupId: 2,
      fallback: {
        filePath: join(pagesDir, "blog.json"),
        initialRevalidate: false,
        initialHeaders: { "content-type": "application/json" },
      },
      config: { allowQuery: [] },
    } as never,
  );

  const adapter = await loadAdapterIn(projectDir);
  await adapter.onBuildComplete(args as never);

  const entry = await readCacheEntry(projectDir, "blog");

  expect(entry.value.kind).toBe("PAGES");
  expect(entry.value.html).toBe("<html>blog</html>");
  expect(entry.value.pageData).toEqual({ pageProps: { title: "blog" } });
});

test("carries the html variant's headers and status onto an APP_PAGE entry", async () => {
  const { projectDir, args } = await synthPrerenderProject();
  args.outputs.prerenders[0].fallback.initialHeaders = {
    "content-type": "text/html; charset=utf-8",
    "x-next-cache-tags": "_N_T_/layout,_N_T_/",
  };
  (args.outputs.prerenders[0].fallback as Record<string, unknown>).initialStatus = 200;
  const adapter = await loadAdapterIn(projectDir);

  await adapter.onBuildComplete(args as never);

  const entry = await readCacheEntry(projectDir, "index");
  expect(entry.value.headers["x-next-cache-tags"]).toBe("_N_T_/layout,_N_T_/");
  expect(entry.value.headers["content-type"]).toBe("text/html; charset=utf-8");
  expect(entry.value.status).toBe(200);
});

test("captures the rsc and segment variants' headers onto an APP_PAGE entry", async () => {
  const { projectDir, args } = await synthPrerenderProject();
  const adapter = await loadAdapterIn(projectDir);

  await adapter.onBuildComplete(args as never);

  const entry = await readCacheEntry(projectDir, "index");
  expect(entry.value.rscHeaders).toEqual({ "content-type": "text/x-component" });
  expect(entry.value.segmentHeaders).toEqual({
    "content-type": "text/x-component",
    "x-nextjs-postponed": "2",
    "x-nextjs-stale-time": "300",
  });
});

async function readVariantHeaders(projectDir: string, bundle = "bundle-0") {
  return JSON.parse(
    await readFile(
      join(projectDir, ".ocel/output/functions", `${bundle}.func`, variantHeadersFile),
      "utf8",
    ),
  );
}

test("ships the build's per-variant headers into every function bundle", async () => {
  const { projectDir, args } = await synthPrerenderProject();
  const adapter = await loadAdapterIn(projectDir);

  await adapter.onBuildComplete(args as never);

  expect(await readVariantHeaders(projectDir)).toEqual({
    index: {
      rscHeaders: { "content-type": "text/x-component" },
      segmentHeaders: {
        "content-type": "text/x-component",
        "x-nextjs-postponed": "2",
        "x-nextjs-stale-time": "300",
      },
    },
  });
});

test("projects only the variant headers, and only for routes that have them", async () => {
  const { projectDir, args } = await synthPrerenderProject();
  const appDir = join(projectDir, ".next/server/app");
  await writeFile(join(appDir, "about.html"), "<html>about</html>");
  args.outputs.prerenders.push({
    pathname: "/about",
    id: "/about",
    type: "PRERENDER",
    parentOutputId: "/",
    groupId: 2,
    fallback: {
      filePath: join(appDir, "about.html"),
      initialRevalidate: false,
      initialHeaders: { "content-type": "text/html; charset=utf-8" },
    },
    config: { allowQuery: [] },
  } as never);
  const adapter = await loadAdapterIn(projectDir);

  await adapter.onBuildComplete(args as never);

  const projection = await readVariantHeaders(projectDir);
  expect(Object.keys(projection)).toEqual(["index"]);
  expect(Object.keys(projection.index)).toEqual([
    "rscHeaders",
    "segmentHeaders",
  ]);
});

test("keeps content-type on an APP_ROUTE cache entry", async () => {
  const projectDir = await mkdtemp(join(tmpdir(), "ocel-next-route-"));
  const handler = join(projectDir, ".next/server/app/api/data/route.js");
  await mkdir(dirname(handler), { recursive: true });
  await writeFile(handler, "module.exports = () => {}");
  const body = join(projectDir, ".next/server/app/api/data.body");
  await writeFile(body, "PAYLOAD");

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
      appPages: [],
      appRoutes: [
        {
          pathname: "/api/data",
          id: "/api/data",
          assets: {},
          runtime: "nodejs",
          filePath: handler,
          config: {},
          type: "APP_ROUTE",
        },
      ],
      staticFiles: [],
      prerenders: [
        {
          pathname: "/api/data",
          id: "/api/data",
          type: "PRERENDER",
          parentOutputId: "/api/data",
          groupId: 1,
          fallback: {
            filePath: body,
            initialStatus: 200,
            initialHeaders: { "content-type": "application/json" },
          },
        },
      ],
    },
    projectDir,
    repoRoot: projectDir,
    distDir: join(projectDir, ".next"),
    config: { basePath: "", images: defaultImages },
    nextVersion: "16.2.10",
    buildId: "test-build",
  };

  const adapter = await loadAdapterIn(projectDir);
  await adapter.onBuildComplete(args as never);

  const entry = await readCacheEntry(projectDir, "api/data");
  expect(entry.value.kind).toBe("APP_ROUTE");
  expect(entry.value.headers["content-type"]).toBe("application/json");
  expect(Buffer.from(entry.value.body, "base64").toString()).toBe("PAYLOAD");
});

test("splits distinct groupIds into separate cache files", async () => {
  const { projectDir, args } = await synthPrerenderProject();
  const appDir = join(projectDir, ".next/server/app");
  await writeFile(join(appDir, "about.html"), "<html>about</html>");
  args.outputs.prerenders.push({
    pathname: "/about",
    id: "/about",
    type: "PRERENDER",
    parentOutputId: "/",
    groupId: 2,
    fallback: {
      filePath: join(appDir, "about.html"),
      initialRevalidate: false,
      initialHeaders: { "content-type": "text/html; charset=utf-8" },
    },
    config: {},
  } as never);
  const adapter = await loadAdapterIn(projectDir);

  await adapter.onBuildComplete(args as never);

  const index = await readCacheEntry(projectDir, "index");
  const about = await readCacheEntry(projectDir, "about");
  expect(index.value.html).toBe("<html>root</html>");
  expect(about.value.html).toBe("<html>about</html>");
});

test("writes every output under OCEL_OUTPUT_DIR when the builder sets it", async () => {
  const { projectDir, args } = await synthPrerenderProject();
  const outputRoot = join(await mkdtemp(join(tmpdir(), "ocel-out-")), "apps/web");
  vi.stubEnv("OCEL_OUTPUT_DIR", outputRoot);
  const adapter = await loadAdapterIn(projectDir);

  await adapter.onBuildComplete(args as never);

  expect(await exists(join(outputRoot, "routing-manifest.json"))).toBe(true);
  expect(await exists(join(outputRoot, "functions/bundle-0.func/config.json"))).toBe(true);
  expect(await exists(join(outputRoot, "cache/index.cache.json"))).toBe(true);
  expect(await exists(join(projectDir, ".ocel/output"))).toBe(false);
});

test("two apps exposing the same route path do not overwrite each other", async () => {
  const outRoot = await mkdtemp(join(tmpdir(), "ocel-two-apps-"));

  for (const app of ["storefront", "admin"]) {
    const { projectDir, args } = await synthProject();
    vi.stubEnv("OCEL_OUTPUT_DIR", join(outRoot, "apps", app));
    vi.stubEnv("OCEL_APP_NAME", app);
    const adapter = await loadAdapterIn(projectDir);
    await adapter.onBuildComplete(args as never);
  }

  for (const app of ["storefront", "admin"]) {
    const outputRoot = join(outRoot, "apps", app);
    const config = JSON.parse(
      await readFile(join(outputRoot, "functions/bundle-0.func/config.json"), "utf8"),
    );
    expect(config.app).toBe(app);
    expect(config.id).toBe("bundle-0");

    const manifest = JSON.parse(await readFile(join(outputRoot, "routing-manifest.json"), "utf8"));
    expect(manifest.appName).toBe(app);
    expect(manifest.dispatch["/api/documents"]).toEqual({
      kind: "lambda",
      id: "bundle-0",
      entryKey: "/api/documents",
    });
    expect(await exists(join(outputRoot, "static/next.svg"))).toBe(true);
  }
});

async function seedFetchCache(
  projectDir: string,
  name: string,
  value: Record<string, unknown>,
  ageMs = 0,
): Promise<void> {
  const dir = join(projectDir, ".next", "cache", "fetch-cache");
  await mkdir(dir, { recursive: true });
  const p = join(dir, name);
  await writeFile(p, JSON.stringify(value));
  if (ageMs > 0) {
    const at = new Date(Date.now() - ageMs);
    await utimes(p, at, at);
  }
}

const fetchHash = "a".repeat(64);

test("seeds fetch-cache entries under their hash, wrapped in an envelope", async () => {
  const { projectDir, args } = await synthProject();
  await seedFetchCache(projectDir, fetchHash, {
    kind: "FETCH",
    data: { body: "upstream", status: 200 },
    revalidate: 900,
    tags: ["posts"],
  });

  const adapter = await loadAdapterIn(projectDir);
  const before = Date.now();
  await adapter.onBuildComplete(args as never);

  const entry = JSON.parse(
    await readFile(
      join(projectDir, ".ocel/output/fetch-cache", `${fetchHash}.cache.json`),
      "utf8",
    ),
  );

  expect(entry.value).toEqual({
    kind: "FETCH",
    data: { body: "upstream", status: 200 },
    revalidate: 900,
    tags: ["posts"],
  });
  expect(entry.lastModified).toBeGreaterThanOrEqual(before);
});

test("stamps fetch entries with build time, not the file's mtime", async () => {
  const { projectDir, args } = await synthProject();
  const weekMs = 7 * 24 * 60 * 60 * 1000;
  await seedFetchCache(
    projectDir,
    fetchHash,
    { kind: "FETCH", data: {}, revalidate: false, tags: [] },
    weekMs,
  );

  const adapter = await loadAdapterIn(projectDir);
  const before = Date.now();
  await adapter.onBuildComplete(args as never);

  const entry = JSON.parse(
    await readFile(
      join(projectDir, ".ocel/output/fetch-cache", `${fetchHash}.cache.json`),
      "utf8",
    ),
  );
  expect(entry.lastModified).toBeGreaterThanOrEqual(before);
});

test("drops fetch entries whose revalidate window already elapsed", async () => {
  const { projectDir, args } = await synthProject();
  await seedFetchCache(
    projectDir,
    fetchHash,
    { kind: "FETCH", data: {}, revalidate: 60, tags: [] },
    10 * 60 * 1000,
  );
  const forced = "b".repeat(64);
  await seedFetchCache(
    projectDir,
    forced,
    { kind: "FETCH", data: {}, revalidate: false, tags: [] },
    10 * 60 * 1000,
  );

  const adapter = await loadAdapterIn(projectDir);
  await adapter.onBuildComplete(args as never);

  const out = join(projectDir, ".ocel/output/fetch-cache");
  expect(await exists(join(out, `${fetchHash}.cache.json`))).toBe(false);
  expect(await exists(join(out, `${forced}.cache.json`))).toBe(true);
});

test("emits no fetch-cache folder for an app that cached no fetch", async () => {
  const { projectDir, args } = await synthProject();
  const adapter = await loadAdapterIn(projectDir);
  await adapter.onBuildComplete(args as never);

  expect(await exists(join(projectDir, ".ocel/output/fetch-cache"))).toBe(false);
});

test("un-normalizes a static Pages Router root output's pathname", async () => {
  const { projectDir, args } = await synthProject();
  const filePath = join(projectDir, ".next/server/pages/index.html");
  await mkdir(dirname(filePath), { recursive: true });
  await writeFile(filePath, "<html>root</html>");
  args.outputs.staticFiles.push({ pathname: "/index", id: "/", filePath });
  const adapter = await loadAdapterIn(projectDir);

  await adapter.onBuildComplete(args as never);

  const manifest = await readManifest(projectDir);
  expect(manifest.dispatch["/"]).toEqual({ kind: "static" });
  expect(manifest.dispatch["/index"]).toBeUndefined();
  expect(manifest.pathnames).toContain("/");
  expect(manifest.pathnames).not.toContain("/index");
  expect(manifest.assetHashes["/index.html"]).toBe(
    createHash("sha256").update("<html>root</html>").digest("hex"),
  );
  expect(
    await readFile(join(projectDir, ".ocel/output/static/index.html"), "utf8"),
  ).toBe("<html>root</html>");
});

test("un-normalizes a getServerSideProps root's dispatch key, keeping its own id as entryKey", async () => {
  const { projectDir, args } = await synthProject();
  const handler = join(projectDir, ".next/server/pages/index.js");
  await mkdir(dirname(handler), { recursive: true });
  await writeFile(handler, "module.exports = () => {}");
  args.outputs.pages.push({
    pathname: "/index",
    id: "/index",
    assets: {},
    runtime: "nodejs",
    filePath: handler,
    config: {},
    type: "PAGES",
  } as never);
  const adapter = await loadAdapterIn(projectDir);

  await adapter.onBuildComplete(args as never);

  const manifest = await readManifest(projectDir);
  expect(manifest.dispatch["/"]).toMatchObject({ kind: "lambda", entryKey: "/index" });
  expect(manifest.dispatch["/index"]).toBeUndefined();
  await expectManifestJoinsLaunchers(projectDir);
});

test("un-normalizes a prerendered root's dispatch key from its PAGES parent, leaving the prerender's own raw pathname alone", async () => {
  const { projectDir, args } = await synthProject();
  args.config = { ...args.config, basePath: "/docs" };
  const handler = join(projectDir, ".next/server/pages/docs/index.js");
  const appDir = join(projectDir, ".next/server/pages/docs");
  await mkdir(appDir, { recursive: true });
  await writeFile(handler, "module.exports = () => {}");
  await writeFile(join(appDir, "index.html"), "<html>root</html>");
  args.outputs.pages.push({
    pathname: "/docs/index",
    id: "/index",
    assets: {},
    runtime: "nodejs",
    filePath: handler,
    config: {},
    type: "PAGES",
  } as never);
  args.outputs.prerenders.push({
    pathname: "/docs",
    id: "/",
    type: "PRERENDER",
    parentOutputId: "/index",
    groupId: 1,
    fallback: {
      filePath: join(appDir, "index.html"),
      initialRevalidate: false,
      initialHeaders: { "content-type": "text/html; charset=utf-8" },
    },
    config: {},
  } as never);
  const adapter = await loadAdapterIn(projectDir);

  await adapter.onBuildComplete(args as never);

  const manifest = await readManifest(projectDir);
  expect(manifest.dispatch["/docs"]).toMatchObject({
    kind: "prerender",
    entryKey: "/index",
  });
  expect(manifest.dispatch["/docs/index"]).toBeUndefined();
  expect(manifest.pathnames).toContain("/docs");
  expect(manifest.pathnames).not.toContain("/docs/index");
  expect(await exists(join(projectDir, ".ocel/output/cache/docs.cache.json"))).toBe(
    true,
  );
  expect(
    await exists(join(projectDir, ".ocel/output/cache/docs/index.cache.json")),
  ).toBe(false);
});

test("un-normalizes a nested pages/index/foo output by dropping one leading /index segment", async () => {
  const { projectDir, args } = await synthProject();
  const handler = join(projectDir, ".next/server/pages/index/index/foo.js");
  await mkdir(dirname(handler), { recursive: true });
  await writeFile(handler, "module.exports = () => {}");
  args.outputs.pages.push({
    pathname: "/index/index/foo",
    id: "/index/index/foo",
    assets: {},
    runtime: "nodejs",
    filePath: handler,
    config: {},
    type: "PAGES",
  } as never);
  const adapter = await loadAdapterIn(projectDir);

  await adapter.onBuildComplete(args as never);

  const manifest = await readManifest(projectDir);
  expect(manifest.dispatch["/index/foo"]).toMatchObject({ kind: "lambda" });
  expect(manifest.dispatch["/index/index/foo"]).toBeUndefined();
  expect(manifest.pathnames).toContain("/index/foo");
});

test("keeps a prerendered pages/index/foo route on its own pathname, not a phantom /foo", async () => {
  const { projectDir, args } = await synthProject();
  const handler = join(projectDir, ".next/server/pages/index/index/foo.js");
  await mkdir(dirname(handler), { recursive: true });
  await writeFile(handler, "module.exports = () => {}");
  await writeFile(join(dirname(handler), "foo.html"), "<html>foo</html>");
  args.outputs.pages.push({
    pathname: "/index/index/foo",
    id: "/index/foo",
    assets: {},
    runtime: "nodejs",
    filePath: handler,
    config: {},
    type: "PAGES",
  } as never);
  args.outputs.prerenders.push({
    pathname: "/index/foo",
    id: "/index/foo",
    type: "PRERENDER",
    parentOutputId: "/index/foo",
    groupId: 1,
    fallback: {
      filePath: join(dirname(handler), "foo.html"),
      initialRevalidate: false,
      initialHeaders: { "content-type": "text/html; charset=utf-8" },
    },
    config: {},
  } as never);
  const adapter = await loadAdapterIn(projectDir);

  await adapter.onBuildComplete(args as never);

  const manifest = await readManifest(projectDir);
  expect(manifest.dispatch["/index/foo"]).toMatchObject({ kind: "prerender" });
  expect(manifest.dispatch["/foo"]).toBeUndefined();
  expect(manifest.pathnames).toContain("/index/foo");
  expect(manifest.pathnames).not.toContain("/foo");
});

test("keeps an edge Pages Router output's already-denormalized pathname addressable by its own name", async () => {
  const { projectDir, args } = await synthProject();
  args.outputs.pages.push(
    {
      pathname: "/index/foo",
      id: "/index/foo",
      assets: {},
      runtime: "edge",
      filePath: join(projectDir, ".next/server/pages/index/foo.js"),
      config: {},
      type: "PAGES",
      edgeRuntime: { entryKey: "pages/index/foo", handlerExport: "default" },
    } as never,
    {
      pathname: "/",
      id: "/",
      assets: {},
      runtime: "edge",
      filePath: join(projectDir, ".next/server/pages/index.js"),
      config: {},
      type: "PAGES",
      edgeRuntime: { entryKey: "pages/index", handlerExport: "default" },
    } as never,
  );
  const adapter = await loadAdapterIn(projectDir);

  await adapter.onBuildComplete(args as never);

  const manifest = await readManifest(projectDir);
  expect(manifest.dispatch["/index/foo"]).toMatchObject({ kind: "edge" });
  expect(manifest.dispatch["/foo"]).toBeUndefined();
  expect(manifest.dispatch["/"]).toMatchObject({ kind: "edge" });
  expect(manifest.pathnames).toContain("/index/foo");
});

test("only translates a STATIC_FILE named under server/pages/, not any /index-shaped pathname", async () => {
  const { projectDir, args } = await synthProject();
  await addStaticOutput(
    args,
    "/index/foo",
    join(projectDir, "out/index/foo.html"),
    "EXPORT FOO",
  );
  await addStaticOutput(
    args,
    "/en",
    join(projectDir, ".next/server/pages/en.html"),
    "LOCALE EN",
  );
  await addStaticOutput(
    args,
    "/404",
    join(projectDir, ".next/server/pages/404.html"),
    "NOT FOUND",
  );
  const adapter = await loadAdapterIn(projectDir);

  await adapter.onBuildComplete(args as never);

  const manifest = await readManifest(projectDir);
  expect(manifest.dispatch["/index/foo"]).toEqual({ kind: "static" });
  expect(manifest.dispatch["/foo"]).toBeUndefined();
  expect(manifest.pathnames).toContain("/index/foo");
  expect(manifest.dispatch["/en"]).toEqual({ kind: "static" });
  expect(manifest.dispatch["/404"]).toEqual({ kind: "static" });
});

test("resolves the App Router not-found page (/_not-found) as errorRoutes.notFound", async () => {
  const { projectDir, args } = await synthProject();
  const notFoundHandler = join(projectDir, ".next/server/app/_not-found.js");
  await mkdir(dirname(notFoundHandler), { recursive: true });
  await writeFile(notFoundHandler, "module.exports = () => {}");
  args.outputs.appPages.push({
    pathname: "/_not-found",
    id: "/_not-found",
    assets: {},
    runtime: "nodejs",
    filePath: notFoundHandler,
    config: {},
    type: "APP_PAGE",
  } as never);
  const adapter = await loadAdapterIn(projectDir);

  await adapter.onBuildComplete(args as never);

  const manifest = await readManifest(projectDir);
  expect(manifest.dispatch["/_not-found"]).toMatchObject({
    kind: "lambda",
    page: true,
  });
  expect(manifest.errorRoutes).toMatchObject({ notFound: "/_not-found" });
});

test("names /_not-found as errorRoutes.notFoundFlight even when /404 wins notFound", async () => {
  const { projectDir, args } = await synthProject();
  await withStaticPage(projectDir, args, "/404", "not found");
  const notFoundHandler = join(projectDir, ".next/server/app/_not-found.js");
  await mkdir(dirname(notFoundHandler), { recursive: true });
  await writeFile(notFoundHandler, "module.exports = () => {}");
  args.outputs.appPages.push({
    pathname: "/_not-found",
    id: "/_not-found",
    assets: {},
    runtime: "nodejs",
    filePath: notFoundHandler,
    config: {},
    type: "APP_PAGE",
  } as never);
  const adapter = await loadAdapterIn(projectDir);

  await adapter.onBuildComplete(args as never);

  const manifest = await readManifest(projectDir);
  expect(manifest.errorRoutes).toMatchObject({
    notFound: "/404",
    notFoundFlight: "/_not-found",
  });
});

test("names no notFoundFlight when the build has no app-router not-found page", async () => {
  const { projectDir, args } = await synthProject();
  await withStaticPage(projectDir, args, "/404", "not found");
  const adapter = await loadAdapterIn(projectDir);

  await adapter.onBuildComplete(args as never);

  const manifest = await readManifest(projectDir);
  expect(manifest.errorRoutes).toMatchObject({ notFound: "/404" });
  expect(manifest.errorRoutes.notFoundFlight).toBeUndefined();
});

test("resolves a static-kind /500 page as errorRoutes.serverError", async () => {
  const { projectDir, args } = await synthProject();
  await withStaticPage(projectDir, args, "/500", "server error");
  const adapter = await loadAdapterIn(projectDir);

  await adapter.onBuildComplete(args as never);

  const manifest = await readManifest(projectDir);
  expect(manifest.dispatch["/500"]).toEqual({ kind: "static" });
  expect(manifest.errorRoutes).toMatchObject({ serverError: "/500" });
});

test("leaves .rsc, _next/data, _next/static and public/ pathnames untouched", async () => {
  const { projectDir, args } = await synthProject();
  await withStaticFile(projectDir, args, "/index.rsc", "RSC-ROOT");
  await withStaticFile(
    projectDir,
    args,
    "/_next/data/test-build/index.json",
    "{}",
  );
  await withStaticFile(projectDir, args, "/_next/static/chunks/a.js", "JS");
  await mkdir(join(projectDir, "public"), { recursive: true });
  await writeFile(join(projectDir, "public", "index.txt"), "public root file");
  const adapter = await loadAdapterIn(projectDir);

  await adapter.onBuildComplete(args as never);

  const manifest = await readManifest(projectDir);
  for (const pathname of [
    "/index.rsc",
    "/_next/data/test-build/index.json",
    "/_next/static/chunks/a.js",
    "/index.txt",
  ]) {
    expect(manifest.dispatch[pathname]).toEqual({ kind: "static" });
    expect(manifest.pathnames).toContain(pathname);
  }
});

test("leaves an App Router route literally named /index untouched", async () => {
  const { projectDir, args } = await synthProject();
  const handler = join(projectDir, ".next/server/app/index/page.js");
  await mkdir(dirname(handler), { recursive: true });
  await writeFile(handler, "module.exports = () => {}");
  args.outputs.appPages.push({
    pathname: "/index",
    id: "/index",
    assets: {},
    runtime: "nodejs",
    filePath: handler,
    config: {},
    type: "APP_PAGE",
  } as never);
  const adapter = await loadAdapterIn(projectDir);

  await adapter.onBuildComplete(args as never);

  const manifest = await readManifest(projectDir);
  expect(manifest.dispatch["/index"]).toMatchObject({ kind: "lambda" });
  expect(manifest.dispatch["/"]).toBeUndefined();
  expect(manifest.pathnames).toContain("/index");
  expect(manifest.pathnames).not.toContain("/");
  await expectManifestJoinsLaunchers(projectDir);
});

test("keeps a dynamic pages/index/[...slug] route addressable by its own name", async () => {
  const { projectDir, args } = await synthProject();
  const handler = join(projectDir, ".next/server/pages/index/[...slug].js");
  await mkdir(dirname(handler), { recursive: true });
  await writeFile(handler, "module.exports = () => {}");
  args.outputs.pages.push({
    pathname: "/index/[...slug]",
    id: "/index/[...slug]",
    assets: {},
    runtime: "nodejs",
    filePath: handler,
    config: {},
    type: "PAGES",
  } as never);
  args.routing.dynamicRoutes.push({
    source: "/index/[...slug]",
    sourceRegex: "^[/]?/index/(?<nxtPslug>.+?)(?:/)?$",
    destination: "/index/[...slug]?nxtPslug=$nxtPslug",
  } as never);
  const adapter = await loadAdapterIn(projectDir);

  await adapter.onBuildComplete(args as never);

  const manifest = await readManifest(projectDir);
  expect(manifest.dispatch["/index/[...slug]"]).toMatchObject({ kind: "lambda" });
  expect(manifest.dispatch["/[...slug]"]).toBeUndefined();
  expect(manifest.pathnames).toContain("/index/[...slug]");

  const { routes } = await readLauncher(projectDir);
  expect(routes.dynamic).toEqual([
    ["^[/]?/index/(?<nxtPslug>.+?)(?:/)?$", "/index/[...slug]"],
  ]);
  await expectManifestJoinsLaunchers(projectDir);
});

test("fails the build when two routes resolve to the same dispatch key", async () => {
  const { projectDir, args } = await synthProject();
  const indexHandler = join(projectDir, ".next/server/pages/index.js");
  const rootHandler = join(projectDir, ".next/server/pages/root.js");
  await mkdir(dirname(indexHandler), { recursive: true });
  await writeFile(indexHandler, "module.exports = () => {}");
  await writeFile(rootHandler, "module.exports = () => {}");
  args.outputs.pages.push(
    {
      pathname: "/index",
      id: "/index",
      assets: {},
      runtime: "nodejs",
      filePath: indexHandler,
      config: {},
      type: "PAGES",
    } as never,
    {
      pathname: "/",
      id: "/",
      assets: {},
      runtime: "nodejs",
      filePath: rootHandler,
      config: {},
      type: "PAGES",
    } as never,
  );
  const adapter = await loadAdapterIn(projectDir);

  await expect(adapter.onBuildComplete(args as never)).rejects.toThrow(
    /both resolve to dispatch key "\/"/,
  );
});

async function withNodeMiddleware(
  projectDir: string,
  args: { outputs: Record<string, unknown> },
  matchers: { source: string; sourceRegex: string }[] = [
    { source: "/__unmatched", sourceRegex: "^/__unmatched$" },
  ],
): Promise<string> {
  const filePath = join(projectDir, ".next/server/middleware.js");
  await mkdir(dirname(filePath), { recursive: true });
  await writeFile(filePath, "module.exports = { default: () => {} }");
  args.outputs.middleware = {
    pathname: "/_middleware",
    id: "/_middleware",
    sourcePage: "middleware",
    assets: {},
    type: "MIDDLEWARE",
    runtime: "nodejs",
    filePath,
    config: { matchers },
  };
  return filePath;
}

test("pins the dispatcher's hardcoded middleware key to the adapter's exported constant", async () => {
  const { middlewareEntryKey } = await import("../src/next-adapter.mts");
  const dispatchSource = await readFile(
    fileURLToPath(new URL("../src/next-dispatch.cjs", import.meta.url)),
    "utf8",
  );
  const literal = dispatchSource.match(/MIDDLEWARE_ENTRY_KEY = "([^"]+)"/)?.[1];
  expect(literal).toBe(middlewareEntryKey);
});

test("names the bundle and the reserved entry key in a nodejs middleware manifest entry", async () => {
  const { projectDir, args } = await synthProject();
  await withNodeMiddleware(projectDir, args, [
    { source: "/dashboard/:path*", sourceRegex: "^/dashboard(?:/(.*))?$" },
  ]);
  const adapter = await loadAdapterIn(projectDir);

  await adapter.onBuildComplete(args as never);

  const manifest = await readManifest(projectDir);
  expect(manifest.middleware).toEqual({
    runtime: "nodejs",
    id: "bundle-0",
    entryKey: "/_middleware",
    matchers: [
      { source: "/dashboard/:path*", sourceRegex: "^/dashboard(?:/(.*))?$" },
    ],
  });
});

test("names an edge middleware manifest entry by its edge entry key", async () => {
  const { projectDir, args } = await synthProject();
  const edgeFile = join(projectDir, ".next/server/edge/middleware.js");
  await mkdir(dirname(edgeFile), { recursive: true });
  await writeFile(edgeFile, "export default () => {}");
  args.outputs.middleware = {
    pathname: "/_middleware",
    id: "middleware",
    sourcePage: "middleware",
    assets: {},
    wasmAssets: {},
    type: "MIDDLEWARE",
    runtime: "edge",
    filePath: edgeFile,
    edgeRuntime: {
      modulePath: edgeFile,
      entryKey: "middleware_middleware",
      handlerExport: "handler",
    },
    config: { matchers: [] },
  } as never;
  const adapter = await loadAdapterIn(projectDir);

  await adapter.onBuildComplete(args as never);

  const manifest = await readManifest(projectDir);
  expect(manifest.middleware).toEqual({
    runtime: "edge",
    entryKey: "middleware_middleware",
    matchers: [],
  });
});

test("injects node middleware's entry and assets into bundle-0 only, including a split build", async () => {
  const { projectDir, args } = await synthDedupProject();
  const filler = join(projectDir, ".next/server/chunks/filler.js");
  await writeFile(filler, "x".repeat(4096));
  (args.outputs.appRoutes[0] as Record<string, unknown>).assets = {
    "chunks/filler.js": filler,
  };
  args.outputs.appRoutes[1]!.assets = args.outputs.appRoutes[0]!.assets;
  const shared = args.outputs.appPages[0]!.assets["chunks/shared.js"]!;
  await writeFile(shared, "x".repeat(4096));
  await withNodeMiddleware(projectDir, args as never);

  process.chdir(projectDir);
  vi.resetModules();
  vi.doMock("../src/pack.mts", async () => {
    const actual =
      await vi.importActual<typeof import("../src/pack.mts")>("../src/pack.mts");
    return {
      ...actual,
      packBundles: (members: never, opts: never) =>
        actual.packBundles(members, { ...(opts as object), budgetBytes: 6000 }),
    };
  });
  try {
    const { default: adapter } = await import("../src/next-adapter.mts");
    await adapter.onBuildComplete(args as never);
  } finally {
    vi.doUnmock("../src/pack.mts");
    vi.resetModules();
  }

  const { real } = await partitionFuncDirs(projectDir);
  expect(real).toEqual(["bundle-0.func", "bundle-1.func"]);

  const { entries: entries0 } = await readLauncher(projectDir, "bundle-0");
  expect(entries0).toHaveProperty("/_middleware", "./.next/server/middleware.js");
  expect(
    await exists(
      join(functionsDir(projectDir), "bundle-0.func/.next/server/middleware.js"),
    ),
  ).toBe(true);

  const { entries: entries1 } = await readLauncher(projectDir, "bundle-1");
  expect(entries1).not.toHaveProperty("/_middleware");
  expect(
    await exists(
      join(functionsDir(projectDir), "bundle-1.func/.next/server/middleware.js"),
    ),
  ).toBe(false);

  const manifest = await readManifest(projectDir);
  expect(manifest.middleware.id).toBe("bundle-0");
});

test("opens its own bundle for node middleware when the build traces zero node routes", async () => {
  const { projectDir, args } = await synthProject();
  args.outputs.appRoutes = [];
  await withNodeMiddleware(projectDir, args);
  const adapter = await loadAdapterIn(projectDir);

  await adapter.onBuildComplete(args as never);

  const { real } = await partitionFuncDirs(projectDir);
  expect(real).toEqual(["bundle-0.func"]);

  const { entries, primary } = await readLauncher(projectDir);
  expect(Object.keys(entries)).toEqual(["/_middleware"]);
  expect(primary).toBeNull();

  const manifest = await readManifest(projectDir);
  expect(manifest.middleware).toMatchObject({ runtime: "nodejs", id: "bundle-0" });
});

test("reports a missing middleware asset in the aggregate no-source warning", async () => {
  const { projectDir, args } = await synthProject();
  await withNodeMiddleware(projectDir, args);
  const ghost = join(projectDir, ".next/server/ghost.js");
  (args.outputs.middleware as { assets: Record<string, string> }).assets = {
    "chunks/ghost.js": ghost,
  };

  const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
  const adapter = await loadAdapterIn(projectDir);
  let lines: string[];
  try {
    await adapter.onBuildComplete(args as never);
  } finally {
    lines = warn.mock.calls.map((c) => String(c[0]));
    warn.mockRestore();
  }

  const noSourceLines = lines.filter((l) => l.includes("no source on disk"));
  expect(noSourceLines).toHaveLength(1);
  expect(noSourceLines[0]).toContain("chunks/ghost.js");
  expect(await exists(join(functionsDir(projectDir), "bundle-0.func/chunks/ghost.js"))).toBe(
    false,
  );
});

test("warns when node middleware's assets push a bundle over the budget", async () => {
  const { projectDir, args } = await synthProject();
  args.outputs.appRoutes = [];
  await withNodeMiddleware(projectDir, args);
  const filePath = (args.outputs.middleware as { filePath: string }).filePath;
  await writeFile(filePath, "x".repeat(8192));

  process.chdir(projectDir);
  vi.resetModules();
  vi.doMock("../src/pack.mts", async () => {
    const actual =
      await vi.importActual<typeof import("../src/pack.mts")>("../src/pack.mts");
    return {
      ...actual,
      packBundles: (members: never, opts: never) =>
        actual.packBundles(members, { ...(opts as object), budgetBytes: 100 }),
    };
  });
  const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
  let lines: string[];
  try {
    const { default: adapter } = await import("../src/next-adapter.mts");
    await adapter.onBuildComplete(args as never);
  } finally {
    lines = warn.mock.calls.map((c) => String(c[0]));
    warn.mockRestore();
    vi.doUnmock("../src/pack.mts");
    vi.resetModules();
  }

  expect(lines.some((l) => l.includes("over the") && l.includes("function limit"))).toBe(true);
});

test("never lets /_middleware enter the launcher's route table", async () => {
  const { projectDir, args } = await synthProject();
  await withNodeMiddleware(projectDir, args);
  const adapter = await loadAdapterIn(projectDir);

  await adapter.onBuildComplete(args as never);

  const { entries, routes } = await readLauncher(projectDir);
  expect(entries).toHaveProperty("/_middleware");
  expect(routes.exact).not.toHaveProperty("/_middleware");
  expect(routes.dynamic.some(([, key]) => key === "/_middleware")).toBe(false);
});

test("warns once when node middleware's matcher covers part of the build's cached surface", async () => {
  const { projectDir, args } = await synthProject();
  await withStaticFile(projectDir, args, "/_next/static/chunks/a.js", "JS");
  await withNodeMiddleware(projectDir, args, [
    { source: "/:path*", sourceRegex: "^/.*$" },
  ]);
  const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
  const adapter = await loadAdapterIn(projectDir);
  let lines: string[];
  try {
    await adapter.onBuildComplete(args as never);
  } finally {
    lines = warn.mock.calls.map((c) => String(c[0]));
    warn.mockRestore();
  }

  const blastRadius = lines.filter((l) => l.includes("matcher covers"));
  expect(blastRadius).toHaveLength(1);
  expect(blastRadius[0]).toContain("round-trip to the function region");
});

test("stays silent when node middleware's matcher covers no cached route", async () => {
  const { projectDir, args } = await synthProject();
  await withStaticFile(projectDir, args, "/_next/static/chunks/a.js", "JS");
  await withNodeMiddleware(projectDir, args, [
    { source: "/only-dynamic", sourceRegex: "^/only-dynamic$" },
  ]);
  const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
  const adapter = await loadAdapterIn(projectDir);
  let lines: string[];
  try {
    await adapter.onBuildComplete(args as never);
  } finally {
    lines = warn.mock.calls.map((c) => String(c[0]));
    warn.mockRestore();
  }

  expect(lines.some((l) => l.includes("matcher covers"))).toBe(false);
});

test("treats an empty matcher list as covering everything for the blast-radius warning", async () => {
  const { projectDir, args } = await synthProject();
  await withStaticFile(projectDir, args, "/_next/static/chunks/a.js", "JS");
  await withNodeMiddleware(projectDir, args, []);
  const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
  const adapter = await loadAdapterIn(projectDir);
  let lines: string[];
  try {
    await adapter.onBuildComplete(args as never);
  } finally {
    lines = warn.mock.calls.map((c) => String(c[0]));
    warn.mockRestore();
  }

  expect(lines.some((l) => l.includes("matcher covers"))).toBe(true);
});
