import {
  mkdtemp,
  mkdir,
  writeFile,
  readFile,
  readdir,
  stat,
  utimes,
} from "node:fs/promises";
import { tmpdir } from "node:os";
import { dirname, join, relative } from "node:path";
import { fileURLToPath } from "node:url";
import {
  PHASE_DEVELOPMENT_SERVER,
  PHASE_PRODUCTION_BUILD,
} from "next/constants.js";
import { afterEach, beforeEach, expect, test, vi } from "vitest";

// Absent OCEL_OUTPUT_DIR, onBuildComplete writes everything under
// process.cwd(), mirroring how the real builder invokes it inside `next build`
// in the app directory. Most tests exercise that fallback: each runs inside a
// throwaway project, chdirs there and imports the adapter fresh, then restores
// the cwd afterward.
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

// A minimal, hermetic build result exercising one nodejs function route plus a
// public/ directory — enough to assert routing/static wiring without depending
// on a real Next build.
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
    config: { basePath: "" },
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

// allFileNames returns the basenames of every file under dir, recursively.
// A missing dir yields no names.
async function allFileNames(dir: string): Promise<string[]> {
  try {
    const entries = await readdir(dir, { recursive: true, withFileTypes: true });
    return entries.filter((e) => e.isFile()).map((e) => e.name);
  } catch {
    return [];
  }
}

// A build result where routes come in base + `.rsc` pairs that share the same
// filePath (and config, assets): the root page (/ and /index.rsc), plus an app
// route (/api/documents and /api/documents.rsc). The root page is also
// prerendered, so its dispatch entry is emitted by the prerenders loop.
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
    config: { basePath: "" },
    nextVersion: "16.2.10",
    buildId: "test-build",
  };

  return { projectDir, args };
}

// A build result centred on prerenders: the root page (/ and /index.rsc) plus
// its PPR segment — all sharing one groupId — each with an on-disk fallback body
// and a rich config, so the tests can assert the group recombines into one
// seeded cache entry carrying html + rscData + segmentData.
async function synthPrerenderProject() {
  const projectDir = await mkdtemp(join(tmpdir(), "ocel-next-isr-"));

  const pageHandler = join(projectDir, ".next/server/app/page.js");
  await mkdir(dirname(pageHandler), { recursive: true });
  await writeFile(pageHandler, "module.exports = () => {}");

  // `next build` writes this manifest; the runtime reads its `config` back as
  // nextConfig, which is the only channel through which the cache handler can
  // be named.
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

  // Fallback bodies Next would have generated under .next/server/app.
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
    config: { basePath: "" },
    nextVersion: "16.2.10",
    buildId: "test-build",
  };

  return { projectDir, args };
}

function functionsDir(projectDir: string): string {
  return join(projectDir, ".ocel/output/functions");
}

// Partitions every `.func` under functions/ into the real directories (one per
// deployed Lambda) and the symlinks (reused variants), each relative to
// functions/ so paths read like "index.func" / "api/documents.func".
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

// Reads a bundle's generated launcher and recovers the two tables it declares.
async function readLauncher(projectDir: string, bundle = "bundle-0") {
  const source = await readFile(
    join(functionsDir(projectDir), `${bundle}.func/__next_launcher.cjs`),
    "utf8",
  );
  const table = (name: string) =>
    JSON.parse(source.match(new RegExp(`^const ${name} = (.*)$`, "m"))![1]!);
  return { source, entries: table("ENTRIES"), primary: table("PRIMARY") };
}

test("packs every node route into one bundle .func", async () => {
  const { projectDir, args } = await synthDedupProject();
  const adapter = await loadAdapterIn(projectDir);

  await adapter.onBuildComplete(args as never);

  const { real, links } = await partitionFuncDirs(projectDir);
  expect(real).toEqual(["bundle-0.func"]);
  // Variants are dispatch entries now, not symlinked functions.
  expect(links).toEqual([]);

  // The bundle carries both members' compiled modules, the shared chunk, the
  // launcher and the dispatcher it requires.
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

// The launcher is the whole bundle's handler: it names every entry by its key
// and hands the table to the dispatcher, which selects one per request.
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
  // AsyncLocalStorage must be a global for Next's runtime, as on the edge.
  expect(source).toContain("globalThis.AsyncLocalStorage = AsyncLocalStorage");
  expect(source).toContain("process.env.NODE_ENV ||= 'production'");
});

// Requiring one entry at INIT primes the chunk graph the bundle shares; the
// largest entry primes the most of it, and the tie-break keeps builds
// byte-identical.
test("primes the largest entry at INIT", async () => {
  const { projectDir, args } = await synthDedupProject();
  const adapter = await loadAdapterIn(projectDir);

  await adapter.onBuildComplete(args as never);

  // "/" traces the shared chunk on top of its own module; the api route only
  // its own.
  expect((await readLauncher(projectDir)).primary).toBe("/");
});

test("gives variants one shared bundle id and one shared entry key", async () => {
  const { projectDir, args } = await synthDedupProject();
  const adapter = await loadAdapterIn(projectDir);

  await adapter.onBuildComplete(args as never);

  const manifest = await readManifest(projectDir);
  // Every route stays a distinct dispatch key, resolving to the same Lambda and
  // the same entry inside it — whether the pair is prerendered (the root page)
  // or rendered on every request (the api route).
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
  // Both variants remain routable.
  expect(manifest.pathnames).toContain("/index.rsc");
  expect(manifest.pathnames).toContain("/api/documents.rsc");
});

// A bundle that overflows the artifact ceiling splits, and each route's dispatch
// entry has to follow its own half.
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

  // Each bundle's launcher declares only its own member.
  expect(Object.keys((await readLauncher(projectDir, "bundle-0")).entries)).toEqual(["/"]);
  expect(
    Object.keys((await readLauncher(projectDir, "bundle-1")).entries),
  ).toEqual(["/api/documents"]);
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
  // The prerender marker replaces the plain lambda entry; the id stays the
  // parent's bundle and the entry key its route inside it, so the runtime can
  // invoke it to regenerate.
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
  // Even the PPR segment, which is a prerender output alone, resolves to its
  // parent's bundle rather than to a route id no function carries.
  expect(manifest.dispatch["/index.segments/_tree.segment.rsc"]).toMatchObject({
    kind: "prerender",
    id: "bundle-0",
    entryKey: "/",
  });
});

// The worker reads `!edgeEntryKey` as "this tier may revalidate", so a node
// prerender must never carry that key — and an edge-parented one must never
// carry a plain entryKey, which would name a Lambda entry that does not exist.
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
});

test("lists every prerender pathname (including .segment.rsc) so resolveRoutes can match it", async () => {
  const { projectDir, args } = await synthPrerenderProject();
  const adapter = await loadAdapterIn(projectDir);

  await adapter.onBuildComplete(args as never);

  const manifest = await readManifest(projectDir);
  // A segment prefetch resolves only if its pathname is in `pathnames`; it lives
  // in `prerenders` alone (never in appPages), so it is the case the old
  // pathnames list dropped — leaving it dispatchable but unresolvable (404).
  expect(manifest.pathnames).toContain("/index.segments/_tree.segment.rsc");
  // Every concrete prerender output must be resolvable, so pathnames is a
  // superset of the prerender dispatch keys.
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
  // Only the two windows survive: the shell, the postponed state and the
  // entry's status/headers all reach the worker through the cache entry.
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

// The id is the functionUrls key the worker looks up, and one bundle serves many
// routes — so it names the bundle, never a route.
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

// Turbopack rewrites config.cacheHandler to a project-relative path, and both it
// and the page-data workers resolve the handler's own `require('next/…')` from
// wherever the file physically sits — from inside a workspace package that is a
// different copy of Next than the app builds with. Copying it into the app's own
// tree is what makes the bare require resolve to the app's Next.
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

// The plural map is bundled per entry into the edge chunks too, and our `use
// cache` handlers reach the AWS SDK transitively — naming them here would drag
// it into every edge bundle. They stay a post-build patch of the node manifest.
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

// The runtime resolves nextConfig.cacheHandler through
// formatDynamicImportPath(distDir, path), which only leaves the value alone
// when it is already absolute — and `next build` rewrites any absolute value in
// this manifest to a path relative to the *build* machine's distDir. Patching
// the built manifest after the fact is what keeps the runtime path intact.
test("names the layer's cache handler by absolute path in required-server-files", async () => {
  const { projectDir, args } = await synthPrerenderProject();
  const adapter = await loadAdapterIn(projectDir);

  await adapter.onBuildComplete(args as never);

  const manifest = JSON.parse(
    await readFile(join(projectDir, ".next/required-server-files.json"), "utf8"),
  );
  expect(manifest.config.cacheHandler).toBe("/opt/ocel/next/cache-handler.cjs");
  // Untouched neighbours prove we patched the manifest rather than rewrote it.
  expect(manifest.config.cacheMaxMemorySize).toBe(0);
  expect(manifest.version).toBe(1);
});

// `use cache` is served by the plural cacheHandlers map, which the runtime
// resolves the same way and therefore needs the same absolute-path treatment.
// Without an entry here the framework's built-in handler is constructed at
// cacheMaxMemorySize 0 — a literal no-op that re-runs every cached function.
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

// Next stores one cache entry per route holding html + rscData + segments
// together, but the adapter API surfaces those as three separate PRERENDER
// outputs (/, /index.rsc, /index.segments/*). Seeding means regrouping them.
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

// The html variant carries the route's real response headers verbatim; the tags
// the cache handler checks on every read ride in on x-next-cache-tags. Each
// variant now owns its own headers map, so the html variant keeps its content-type.
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

// The client gates PPR support on per-variant response headers — above all the
// segment cache's x-nextjs-postponed: 2 marker — so the adapter captures the rsc
// and segment variants' own initialHeaders verbatim, storing the segment headers
// once (they are identical across a group).
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

// An APP_ROUTE stores a single body whose type Next cannot re-derive, so its
// content-type must survive verbatim onto the entry.
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
    config: { basePath: "" },
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

// groupId is 1:1 with a route's cache entry, so two prerendered routes with
// distinct groupIds must land in two separate cache.json files.
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

// Build output is namespaced per app, and the adapter cannot infer which
// subtree is its own — it builds inside the app dir, not the project root. The
// ocel builder tells it via OCEL_OUTPUT_DIR; everything the build emits must
// land there and nowhere else.
test("writes every output under OCEL_OUTPUT_DIR when the builder sets it", async () => {
  const { projectDir, args } = await synthPrerenderProject();
  const outputRoot = join(await mkdtemp(join(tmpdir(), "ocel-out-")), "apps/web");
  vi.stubEnv("OCEL_OUTPUT_DIR", outputRoot);
  const adapter = await loadAdapterIn(projectDir);

  await adapter.onBuildComplete(args as never);

  expect(await exists(join(outputRoot, "routing-manifest.json"))).toBe(true);
  expect(await exists(join(outputRoot, "functions/bundle-0.func/config.json"))).toBe(true);
  expect(await exists(join(outputRoot, "cache/index.cache.json"))).toBe(true);
  // Nothing may fall back to the cwd-derived flat tree.
  expect(await exists(join(projectDir, ".ocel/output"))).toBe(false);
});

// The collision this layout exists to prevent: before it, the second app's
// build overwrote the first's functions, static assets and routing manifest.
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
    // Same bundle name in both apps: the worker dispatches on it per app, so it
    // must NOT be app-qualified.
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

// Next writes fetch/unstable_cache entries as the bare cache value under a hash
// filename, deriving lastModified from the file's mtime. The deployed handler
// reads the stored envelope instead, so the build has to supply one.
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

  // The hash is the handler's lookup key, so the filename must survive verbatim.
  expect(entry.value).toEqual({
    kind: "FETCH",
    data: { body: "upstream", status: 200 },
    revalidate: 900,
    tags: ["posts"],
  });
  expect(entry.lastModified).toBeGreaterThanOrEqual(before);
});

// The pruning proof behind the tag clock rests on every entry in a build having
// lastModified >= that build's deployedAt. .next/cache survives across builds,
// so an mtime carried over from an older one would break it.
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

// Stamping build time restarts an entry's revalidate window, so one whose window
// already elapsed must be dropped rather than shipped with a clock it did not
// earn. force-cache (revalidate: false) has no window and is always kept.
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
