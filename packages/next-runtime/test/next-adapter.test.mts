import {
  mkdtemp,
  mkdir,
  writeFile,
  readFile,
  readdir,
  stat,
  lstat,
  readlink,
  utimes,
} from "node:fs/promises";
import { tmpdir } from "node:os";
import { dirname, join, relative } from "node:path";
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

async function isSymlink(p: string): Promise<boolean> {
  try {
    return (await lstat(p)).isSymbolicLink();
  } catch {
    return false;
  }
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

test("creates one real .func per shared filePath and symlinks the variants", async () => {
  const { projectDir, args } = await synthDedupProject();
  const adapter = await loadAdapterIn(projectDir);

  await adapter.onBuildComplete(args as never);

  const fns = functionsDir(projectDir);
  // Exactly one real .func per group — no accidental extra function.
  const { real, links } = await partitionFuncDirs(projectDir);
  expect(real).toEqual(["api/documents.func", "index.func"]);
  expect(links).toEqual(["api/documents.rsc.func", "index.rsc.func"]);
  // Variants symlink to their sibling parent, relatively.
  expect(await readlink(join(fns, "index.rsc.func"))).toBe("index.func");
  expect(await readlink(join(fns, "api/documents.rsc.func"))).toBe(
    "documents.func",
  );
});

test("variant .func symlinks carry no copied assets or config of their own", async () => {
  const { projectDir, args } = await synthDedupProject();
  const adapter = await loadAdapterIn(projectDir);

  await adapter.onBuildComplete(args as never);

  const fns = functionsDir(projectDir);
  // The variant is purely a symlink — no directory of its own to copy into.
  expect(await isSymlink(join(fns, "index.rsc.func"))).toBe(true);
  // The parent owns the sole config.json, carrying the parent's id; reading it
  // through the variant symlink resolves to that same file.
  const parentCfg = JSON.parse(
    await readFile(join(fns, "index.func/config.json"), "utf8"),
  );
  const viaSymlink = JSON.parse(
    await readFile(join(fns, "index.rsc.func/config.json"), "utf8"),
  );
  expect(parentCfg.id).toBe("/");
  expect(viaSymlink).toEqual(parentCfg);
});

test("repoints variant dispatch ids to the parent function", async () => {
  const { projectDir, args } = await synthDedupProject();
  const adapter = await loadAdapterIn(projectDir);

  await adapter.onBuildComplete(args as never);

  const manifest = await readManifest(projectDir);
  // Every route stays a distinct dispatch key.
  expect(manifest.dispatch["/"].id).toBe("/");
  expect(manifest.dispatch["/index.rsc"].id).toBe("/");
  expect(manifest.dispatch["/api/documents"].id).toBe("/api/documents");
  expect(manifest.dispatch["/api/documents.rsc"].id).toBe("/api/documents");
  // Both variants remain routable.
  expect(manifest.pathnames).toContain("/index.rsc");
  expect(manifest.pathnames).toContain("/api/documents.rsc");
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
  // parent function so the runtime can invoke it to regenerate.
  expect(manifest.dispatch["/"]).toMatchObject({ kind: "prerender", id: "/" });
  expect(manifest.dispatch["/index.rsc"]).toMatchObject({
    kind: "prerender",
    id: "/",
  });
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

test("writes each function's route id into its config.json", async () => {
  const { projectDir, args } = await synthProject();
  const adapter = await loadAdapterIn(projectDir);

  await adapter.onBuildComplete(args as never);

  const config = JSON.parse(
    await readFile(
      join(
        projectDir,
        ".ocel/output/functions/api/documents.func/config.json",
      ),
      "utf8",
    ),
  );

  expect(config.id).toBe("/api/documents");
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
      join(projectDir, ".ocel/output/functions/api/documents.func/config.json"),
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

// The html variant carries the route's real response headers; the tags the
// cache handler checks on every read ride in on x-next-cache-tags. Content-type
// is dropped for an APP_PAGE — Next derives it per-variant at serve time, so a
// stored text/html would mislabel the RSC and segment reads sharing this entry.
test("carries the html variant's headers (sans content-type) and status onto an APP_PAGE entry", async () => {
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
  expect(entry.value.headers["content-type"]).toBeUndefined();
  expect(entry.value.status).toBe(200);
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
  expect(await exists(join(outputRoot, "functions/index.func/config.json"))).toBe(true);
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
      await readFile(join(outputRoot, "functions/api/documents.func/config.json"), "utf8"),
    );
    expect(config.app).toBe(app);
    // Same route id in both apps: the worker dispatches on it, so it must NOT
    // be app-qualified.
    expect(config.id).toBe("/api/documents");

    const manifest = JSON.parse(await readFile(join(outputRoot, "routing-manifest.json"), "utf8"));
    expect(manifest.appName).toBe(app);
    expect(manifest.dispatch["/api/documents"]).toEqual({ kind: "lambda", id: "/api/documents" });
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
