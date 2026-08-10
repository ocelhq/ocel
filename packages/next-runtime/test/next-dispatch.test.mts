import Module, { createRequire } from "node:module";
import fs, { mkdirSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { Writable } from "node:stream";
import type http from "node:http";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { afterEach, expect, test, vi } from "vitest";

const dispatchPath = fileURLToPath(
  new URL("../src/next-dispatch.cjs", import.meta.url),
);
const require = createRequire(import.meta.url);
const createDispatch = require(dispatchPath);

const ENTRIES = {
  "app/page": "./.next/server/app/page.js",
  "app/api/x/route": "./.next/server/app/api/x/route.js",
};

function fakeLoader() {
  const loads: string[] = [];
  const load = (path: string) => {
    loads.push(path);
    return { handler: (...args: unknown[]) => ({ path, args }) };
  };
  return { loads, load };
}

function throwingLoader(broken: string, error = new Error("boom")) {
  const loads: string[] = [];
  const load = (path: string) => {
    loads.push(path);
    if (path === broken) throw error;
    return { handler: (...args: unknown[]) => ({ path, args }) };
  };
  return { loads, load };
}

function silenceErrors() {
  return vi.spyOn(console, "error").mockImplementation(() => {});
}

// The compile-cache APIs live on the node:module default export, which the
// dispatcher reads at call time; patching it there is what the CJS require of
// the source under test sees. Deleting a key (rather than assigning undefined)
// is what makes a case a genuinely absent API, as on older Node.
const moduleApis = Module as unknown as Record<string, unknown>;
const savedModuleApis = new Map<string, unknown>();

function patchModule(patch: Record<string, unknown>): void {
  for (const [key, value] of Object.entries(patch)) {
    if (!savedModuleApis.has(key)) savedModuleApis.set(key, moduleApis[key]);
    if (value === undefined) delete moduleApis[key];
    else moduleApis[key] = value;
  }
}

let cacheDir: string | null = null;
let flushes = 0;
let pending: { name: string; size: number }[] = [];

// V8 buffers compile-cache writes in memory until a flush, so the stub only
// materializes a load's bytes when flushCompileCache runs — a measurement taken
// without flushing first reads the stale directory, exactly as in production.
function stubCompileCache(patch: Record<string, unknown> = {}): string {
  cacheDir = mkdtempSync(join(tmpdir(), "ocel-warm-"));
  patchModule({
    getCompileCacheDir: () => cacheDir,
    flushCompileCache: () => {
      flushes += 1;
      for (const file of pending.splice(0)) {
        const target = join(cacheDir!, file.name);
        mkdirSync(dirname(target), { recursive: true });
        writeFileSync(target, Buffer.alloc(file.size));
      }
    },
    ...patch,
  });
  return cacheDir;
}

function cachingLoader(sizes: Record<string, number>, broken: string[] = []) {
  const loads: string[] = [];
  const load = (path: string) => {
    loads.push(path);
    pending.push({ name: `blobs/${loads.length}.blob`, size: sizes[path] ?? 0 });
    if (broken.includes(path)) throw new Error(`boom ${path}`);
    return { handler: () => path };
  };
  return { loads, load };
}

const WARM_ENTRIES = {
  "app/page": "./.next/server/app/page.js",
  "app/a/page": "./.next/server/app/a/page.js",
  "app/b/page": "./.next/server/app/b/page.js",
};

const NO_LIMITS = { deadlineMs: Date.now() + 600_000, ceilingBytes: 64 << 20 };

afterEach(() => {
  vi.restoreAllMocks();
  for (const [key, value] of savedModuleApis) {
    if (value === undefined) delete moduleApis[key];
    else moduleApis[key] = value;
  }
  savedModuleApis.clear();
  if (cacheDir) rmSync(cacheDir, { recursive: true, force: true });
  cacheDir = null;
  flushes = 0;
  pending = [];
});

function fakeReq(entry?: string, url = "/") {
  return { url, headers: entry === undefined ? {} : { "x-ocel-entry": entry } };
}

function fakeRes() {
  return {
    statusCode: 200,
    body: undefined as unknown,
    end(chunk?: unknown) {
      this.body = chunk;
    },
  };
}

test("loads the primary entry once while the launcher is still being required", () => {
  const { loads, load } = fakeLoader();

  createDispatch({ entries: ENTRIES, primary: "app/page", load });

  expect(loads).toEqual([ENTRIES["app/page"]]);
});

test("loads nothing eagerly without a primary", () => {
  const { loads, load } = fakeLoader();

  createDispatch({ entries: ENTRIES, primary: null, load });

  expect(loads).toEqual([]);
});

test("loads a lazy entry on its first request and never again", async () => {
  const { loads, load } = fakeLoader();
  const dispatch = createDispatch({ entries: ENTRIES, primary: null, load });

  await dispatch.handler(fakeReq("app/api/x/route"), fakeRes(), {});
  await dispatch.handler(fakeReq("app/api/x/route"), fakeRes(), {});

  expect(loads).toEqual([ENTRIES["app/api/x/route"]]);
});

test("reuses the eagerly loaded primary when a request hits it", async () => {
  const { loads, load } = fakeLoader();
  const dispatch = createDispatch({ entries: ENTRIES, primary: "app/page", load });

  await dispatch.handler(fakeReq("app/page"), fakeRes(), {});

  expect(loads).toEqual([ENTRIES["app/page"]]);
});

test("passes req, res and ctx through to the entry untouched", async () => {
  const seen: unknown[] = [];
  const dispatch = createDispatch({
    entries: ENTRIES,
    primary: null,
    load: () => ({
      handler: (...args: unknown[]) => {
        seen.push(args);
        return "returned";
      },
    }),
  });
  const req = fakeReq("app/page");
  const res = fakeRes();
  const ctx = { waitUntil: () => {}, requestMeta: {} };

  const result = await dispatch.handler(req, res, ctx);

  expect(result).toBe("returned");
  expect(seen).toEqual([[req, res, ctx]]);
  expect(res.statusCode).toBe(200);
});

test("fails closed on a request with no entry header and no route for its path", async () => {
  const { load } = fakeLoader();
  const dispatch = createDispatch({ entries: ENTRIES, primary: null, load });
  const res = fakeRes();

  await dispatch.handler(fakeReq(undefined, "/nowhere"), res, {});

  expect(res.statusCode).toBe(502);
  expect(res.body).toMatch(/no entry serves \/nowhere/);
});

// The app's own loopback self-fetches — Next running a Server Action that lives
// on another page, and following an action's redirect — reach the dispatcher
// with no entry header, because the membrane strips the stale one they
// inherited. Without pathname resolution they would run the originating route.
const ROUTES = {
  exact: { "/server": "/server", "/header": "/header" },
  dynamic: [["^/blog/([^/]+?)(?:\\.rsc)?$", "/blog/[slug]"]] as [string, string][],
};

const ROUTED = {
  ...ENTRIES,
  "/server": "./.next/server/app/server/page.js",
  "/header": "./.next/server/app/header/page.js",
  "/blog/[slug]": "./.next/server/app/blog/[slug]/page.js",
};

// The shape of the regression: an action on /server redirects to /header, Next
// follows it by fetching this process, and the membrane has stripped the entry
// key that request inherited — which named /server. It must land on /header.
test("a self-fetch is served by the route it asks for, not the one it came from", async () => {
  const { loads, load } = fakeLoader();
  const dispatch = createDispatch({
    entries: ROUTED,
    primary: null,
    routes: ROUTES,
    load,
  });

  const result = await dispatch.handler(
    fakeReq(undefined, "/header?result=122"),
    fakeRes(),
    {},
  );

  expect(result).toMatchObject({ path: ROUTED["/header"] });
  expect(loads).not.toContain(ROUTED["/server"]);
});

test("a dynamic route serves a concrete pathname beneath it", async () => {
  const { load } = fakeLoader();
  const dispatch = createDispatch({
    entries: ROUTED,
    primary: null,
    routes: ROUTES,
    load,
  });

  const result = await dispatch.handler(fakeReq(undefined, "/blog/hello"), fakeRes(), {});

  expect(result).toMatchObject({ path: ROUTED["/blog/[slug]"] });
});

test("an exact route wins over a dynamic one that also matches", async () => {
  const { load } = fakeLoader();
  const dispatch = createDispatch({
    entries: ROUTED,
    primary: null,
    routes: {
      exact: { "/blog/featured": "/header" },
      dynamic: ROUTES.dynamic,
    },
    load,
  });

  const result = await dispatch.handler(fakeReq(undefined, "/blog/featured"), fakeRes(), {});

  expect(result).toMatchObject({ path: ROUTED["/header"] });
});

test("a named request is served by the name, never by its pathname", async () => {
  const { load } = fakeLoader();
  const dispatch = createDispatch({
    entries: ROUTED,
    primary: null,
    routes: ROUTES,
    load,
  });

  const result = await dispatch.handler(fakeReq("/server", "/header"), fakeRes(), {});

  expect(result).toMatchObject({ path: ROUTED["/server"] });
});

// Next prefixes basePath onto every output pathname before the adapter sees one
// (normalizePathnames, build-complete.ts:537-556), and onto both the dynamic
// patterns and the URLs the app self-fetches. So a basePath build's table is
// simply spelled with it, on both sides, and the dispatcher adds and strips
// nothing. This table is that shape.
test("a basePath route is served under the path the build gave it", async () => {
  const { load } = fakeLoader();
  const dispatch = createDispatch({
    entries: { "/docs/header": "./.next/server/app/header/page.js" },
    primary: null,
    routes: {
      exact: { "/docs/header": "/docs/header" },
      dynamic: [["^/docs[/]?/blog/(?<nxtPslug>[^/]+?)(?:/)?$", "/docs/blog/[slug]"]],
    },
    load,
  });

  const result = await dispatch.handler(fakeReq(undefined, "/docs/header?x=1"), fakeRes(), {});

  expect(result).toMatchObject({ path: "./.next/server/app/header/page.js" });
});

// The table names every route the build has, not just the ones this bundle
// carries, so a pathname another bundle owns is refused by name instead of
// being absorbed by a local pattern broad enough to span it.
test("a pathname another bundle owns fails by name, not by a local catch-all", async () => {
  const { loads, load } = fakeLoader();
  const dispatch = createDispatch({
    entries: { "/files/[...path]": "./.next/server/app/files/[...path]/page.js" },
    primary: null,
    routes: {
      exact: { "/elsewhere": "/elsewhere" },
      dynamic: [["^[/]?/(?<nxtPpath>.+?)(?:/)?$", "/files/[...path]"]],
    },
    load,
  });
  const res = fakeRes();

  await dispatch.handler(fakeReq(undefined, "/elsewhere"), res, {});

  expect(res.statusCode).toBe(502);
  expect(res.body).toMatch(/\/elsewhere/);
  expect(loads).toEqual([]);
});

test("fails closed on a key the bundle does not carry, naming it", async () => {
  const { loads, load } = fakeLoader();
  const dispatch = createDispatch({ entries: ENTRIES, primary: null, load });
  const res = fakeRes();

  await dispatch.handler(fakeReq("app/ghost/page"), res, {});

  expect(res.statusCode).toBe(502);
  expect(res.body).toMatch(/app\/ghost\/page/);
  expect(loads).toEqual([]);
});

test("fails closed on an entry module that exports no handler, naming the key", async () => {
  const dispatch = createDispatch({
    entries: ENTRIES,
    primary: null,
    load: () => ({}),
  });
  const res = fakeRes();

  await dispatch.handler(fakeReq("app/page"), res, {});

  expect(res.statusCode).toBe(502);
  expect(res.body).toMatch(/app\/page/);
});

test("a primary that throws on load neither kills the factory nor the other entries", async () => {
  const errors = silenceErrors();
  const { load } = throwingLoader(ENTRIES["app/page"]);

  const dispatch = createDispatch({ entries: ENTRIES, primary: "app/page", load });
  const res = fakeRes();
  const result = await dispatch.handler(fakeReq("app/api/x/route"), res, {});

  expect(res.statusCode).toBe(200);
  expect(result).toMatchObject({ path: ENTRIES["app/api/x/route"] });
  expect(errors).toHaveBeenCalled();
  expect(String(errors.mock.calls[0]?.[0])).toMatch(/app\/page/);
});

test("the broken primary's own key fails closed, naming it", async () => {
  silenceErrors();
  const { load } = throwingLoader(ENTRIES["app/page"]);
  const dispatch = createDispatch({ entries: ENTRIES, primary: "app/page", load });
  const res = fakeRes();

  await dispatch.handler(fakeReq("app/page"), res, {});

  expect(res.statusCode).toBe(502);
  expect(res.body).toMatch(/app\/page/);
  expect(res.body).toMatch(/boom/);
});

test("a lazy entry that throws on load fails closed instead of propagating", async () => {
  silenceErrors();
  const { load } = throwingLoader(ENTRIES["app/api/x/route"]);
  const dispatch = createDispatch({ entries: ENTRIES, primary: null, load });
  const res = fakeRes();

  await dispatch.handler(fakeReq("app/api/x/route"), res, {});

  expect(res.statusCode).toBe(502);
  expect(res.body).toMatch(/app\/api\/x\/route/);
});

test("memoizes a failed load so a doomed require is never re-attempted", async () => {
  silenceErrors();
  const { loads, load } = throwingLoader(ENTRIES["app/api/x/route"]);
  const dispatch = createDispatch({ entries: ENTRIES, primary: null, load });

  await dispatch.handler(fakeReq("app/api/x/route"), fakeRes(), {});
  const second = fakeRes();
  await dispatch.handler(fakeReq("app/api/x/route"), second, {});

  expect(loads).toEqual([ENTRIES["app/api/x/route"]]);
  expect(second.statusCode).toBe(502);
  expect(second.body).toMatch(/app\/api\/x\/route/);
});

test("memoizes a failed prime so the broken primary is required only at init", async () => {
  silenceErrors();
  const { loads, load } = throwingLoader(ENTRIES["app/page"]);
  const dispatch = createDispatch({ entries: ENTRIES, primary: "app/page", load });

  await dispatch.handler(fakeReq("app/page"), fakeRes(), {});
  await dispatch.handler(fakeReq("app/page"), fakeRes(), {});

  expect(loads).toEqual([ENTRIES["app/page"]]);
});

test("warms every entry, primary first and then in table order", () => {
  const dir = stubCompileCache();
  const { loads, load } = cachingLoader({});
  const dispatch = createDispatch({
    entries: WARM_ENTRIES,
    primary: "app/a/page",
    load,
  });

  const report = dispatch.warm(NO_LIMITS);

  expect(loads).toEqual([
    WARM_ENTRIES["app/a/page"],
    WARM_ENTRIES["app/page"],
    WARM_ENTRIES["app/b/page"],
  ]);
  expect(report).toMatchObject({
    ok: true,
    state: "warmed",
    entries: 3,
    loaded: 3,
    failures: [],
    stoppedBy: "complete",
    dir,
  });
});

test("reuses the primary's init load rather than requiring it twice", () => {
  stubCompileCache();
  const { loads, load } = cachingLoader({});
  const dispatch = createDispatch({
    entries: WARM_ENTRIES,
    primary: "app/page",
    load,
  });

  dispatch.warm(NO_LIMITS);

  expect(loads.filter((p) => p === WARM_ENTRIES["app/page"])).toHaveLength(1);
});

test("serves a warmed entry without requiring it again", async () => {
  stubCompileCache();
  const { loads, load } = cachingLoader({});
  const dispatch = createDispatch({ entries: WARM_ENTRIES, primary: null, load });

  dispatch.warm(NO_LIMITS);
  await dispatch.handler(fakeReq("app/b/page"), fakeRes(), {});

  expect(loads.filter((p) => p === WARM_ENTRIES["app/b/page"])).toHaveLength(1);
});

test("reports a warm-time failure without abandoning the rest of the bundle", () => {
  stubCompileCache();
  const { load } = cachingLoader({}, [WARM_ENTRIES["app/a/page"]]);
  const dispatch = createDispatch({ entries: WARM_ENTRIES, primary: null, load });

  const report = dispatch.warm(NO_LIMITS);

  expect(report.failures).toEqual([
    { entry: "app/a/page", message: `boom ${WARM_ENTRIES["app/a/page"]}` },
  ]);
  expect(report.loaded).toBe(2);
  expect(report.stoppedBy).toBe("complete");
});

test("does not memoize a warm-time failure, so the request path retries it", async () => {
  stubCompileCache();
  const loads: string[] = [];
  let attempts = 0;
  const load = (path: string) => {
    loads.push(path);
    if (path === WARM_ENTRIES["app/a/page"] && attempts++ === 0) {
      throw new Error("boom");
    }
    return { handler: () => path };
  };
  const dispatch = createDispatch({ entries: WARM_ENTRIES, primary: null, load });

  dispatch.warm(NO_LIMITS);
  const res = fakeRes();
  const result = await dispatch.handler(fakeReq("app/a/page"), res, {});

  expect(res.statusCode).toBe(200);
  expect(result).toBe(WARM_ENTRIES["app/a/page"]);
  expect(loads.filter((p) => p === WARM_ENTRIES["app/a/page"])).toHaveLength(2);
});

test("stops between entries once the deadline has passed", () => {
  stubCompileCache();
  let now = 1000;
  vi.spyOn(Date, "now").mockImplementation(() => now);
  const { loads, load } = cachingLoader({});
  const dispatch = createDispatch({
    entries: WARM_ENTRIES,
    primary: "app/page",
    load: (path: string) => {
      now += 10;
      return load(path);
    },
  });

  const report = dispatch.warm({ deadlineMs: 1015, ceilingBytes: 64 << 20 });

  expect(loads).toEqual([
    WARM_ENTRIES["app/page"],
    WARM_ENTRIES["app/a/page"],
  ]);
  expect(report).toMatchObject({ loaded: 2, stoppedBy: "deadline" });
});

test("stops before the ceiling, predicting the next entry by the largest growth so far", () => {
  stubCompileCache();
  const { loads, load } = cachingLoader({
    [WARM_ENTRIES["app/page"]]: 1000,
    [WARM_ENTRIES["app/a/page"]]: 1000,
    [WARM_ENTRIES["app/b/page"]]: 1000,
  });
  const dispatch = createDispatch({
    entries: WARM_ENTRIES,
    primary: "app/page",
    load,
  });

  const report = dispatch.warm({ deadlineMs: Date.now() + 600_000, ceilingBytes: 4096 });

  expect(loads).toEqual([
    WARM_ENTRIES["app/page"],
    WARM_ENTRIES["app/a/page"],
  ]);
  expect(report).toMatchObject({ loaded: 2, stoppedBy: "ceiling", bytes: 3024 });
});

test("charges 512 bytes per cached file, matching what the uploader charges", () => {
  stubCompileCache();
  const { load } = cachingLoader({
    [WARM_ENTRIES["app/page"]]: 100,
    [WARM_ENTRIES["app/a/page"]]: 250,
    [WARM_ENTRIES["app/b/page"]]: 30,
  });
  const dispatch = createDispatch({
    entries: WARM_ENTRIES,
    primary: "app/page",
    load,
  });

  const report = dispatch.warm(NO_LIMITS);

  expect(report.bytes).toBe(100 + 250 + 30 + 3 * 512);
});

test("flushes before every measurement, including the last entry's", () => {
  stubCompileCache();
  const { load } = cachingLoader({});
  const dispatch = createDispatch({
    entries: WARM_ENTRIES,
    primary: "app/page",
    load,
  });

  dispatch.warm(NO_LIMITS);

  expect(flushes).toBe(3);
});

test("reports unsupported when flushCompileCache is genuinely absent (old Node)", () => {
  stubCompileCache({ flushCompileCache: undefined });
  const { loads, load } = cachingLoader({});
  const dispatch = createDispatch({ entries: WARM_ENTRIES, primary: null, load });

  expect(dispatch.warm(NO_LIMITS)).toEqual({
    ok: false,
    state: "unsupported",
    entries: 3,
    loaded: 0,
    failures: [],
    stoppedBy: "complete",
    skipped: [],
    skippedCount: 0,
    bytes: 0,
    dir: null,
  });
  expect(loads).toEqual([]);
});

test("reports unsupported when getCompileCacheDir is genuinely absent (old Node)", () => {
  stubCompileCache({ getCompileCacheDir: undefined });
  const { load } = cachingLoader({});
  const dispatch = createDispatch({ entries: WARM_ENTRIES, primary: null, load });

  expect(dispatch.warm(NO_LIMITS)).toMatchObject({
    ok: false,
    state: "unsupported",
    dir: null,
  });
});

test("reports unsupported when the compile cache is off, leaving no dir", () => {
  stubCompileCache({ getCompileCacheDir: () => "" });
  const { load } = cachingLoader({});
  const dispatch = createDispatch({ entries: WARM_ENTRIES, primary: null, load });

  expect(dispatch.warm(NO_LIMITS)).toMatchObject({
    ok: false,
    state: "unsupported",
  });
});

// A stopped walk that reports only "38/51" leaves an operator with no way to
// tell which routes stay cold, which is the one thing the report exists for.
test("names the entries a stopped walk never reached", () => {
  stubCompileCache();
  const { load } = cachingLoader({
    [WARM_ENTRIES["app/page"]]: 1000,
    [WARM_ENTRIES["app/a/page"]]: 1000,
    [WARM_ENTRIES["app/b/page"]]: 1000,
  });
  const dispatch = createDispatch({ entries: WARM_ENTRIES, primary: "app/page", load });

  const report = dispatch.warm({ deadlineMs: Date.now() + 600_000, ceilingBytes: 4096 });

  expect(report).toMatchObject({
    stoppedBy: "ceiling",
    skipped: ["app/b/page"],
    skippedCount: 1,
  });
});

test("a walk that reached every entry skips nothing", () => {
  stubCompileCache();
  const { load } = cachingLoader({});
  const dispatch = createDispatch({ entries: WARM_ENTRIES, primary: "app/page", load });

  expect(dispatch.warm(NO_LIMITS)).toMatchObject({ skipped: [], skippedCount: 0 });
});

// The list travels through a control message, a CloudWatch line and the
// deploy's output, so a bundle with hundreds of cold routes must not be able to
// bloat any of them — while the count still tells the whole truth.
test("bounds the skipped list but not the skipped count", () => {
  stubCompileCache();
  const entries: Record<string, string> = { "app/page": "./.next/server/app/page.js" };
  for (let i = 0; i < 60; i++) entries[`app/r${i}/page`] = `./.next/server/app/r${i}/page.js`;
  const { load } = cachingLoader({});
  const dispatch = createDispatch({ entries, primary: "app/page", load });

  // A deadline already in the past stops the walk before its first entry.
  const report = dispatch.warm({ deadlineMs: Date.now() - 1, ceilingBytes: 64 << 20 });

  expect(report.skippedCount).toBe(60);
  expect(report.skipped).toHaveLength(20);
  expect(report.skipped[0]).toBe("app/r0/page");
});

// Measuring is a flush plus a full recursive walk of the cache directory, so
// one per entry is O(entries x files) inside the very deadline the walk exists
// to respect — and it buys nothing while the total is nowhere near the ceiling.
test("measures in strides while the ceiling is far away", () => {
  stubCompileCache();
  const entries: Record<string, string> = { "app/page": "./.next/server/app/page.js" };
  for (let i = 0; i < 40; i++) entries[`app/r${i}/page`] = `./.next/server/app/r${i}/page.js`;
  const { load } = cachingLoader({});
  const dispatch = createDispatch({ entries, primary: "app/page", load });

  const report = dispatch.warm(NO_LIMITS);

  expect(report.loaded).toBe(41);
  expect(report.stoppedBy).toBe("complete");
  expect(flushes).toBeLessThan(15);
});

// The ceiling guarantee survives the stride: the stride is sized against the
// largest growth yet seen, and shrinks to a single entry as the headroom does.
test("still stops before the ceiling when entries are large", () => {
  stubCompileCache();
  const entries: Record<string, string> = {};
  const sizes: Record<string, number> = {};
  for (let i = 0; i < 20; i++) {
    entries[`app/r${i}/page`] = `./.next/server/app/r${i}/page.js`;
    sizes[`./.next/server/app/r${i}/page.js`] = 1000;
  }
  const { loads, load } = cachingLoader(sizes);
  const dispatch = createDispatch({ entries, primary: null, load });

  const report = dispatch.warm({ deadlineMs: Date.now() + 600_000, ceilingBytes: 8192 });

  expect(report.stoppedBy).toBe("ceiling");
  expect(report.bytes).toBeLessThanOrEqual(8192);
  expect(loads.length).toBeLessThan(20);
});

// An unreadable cache directory must not measure as a partial total: the walk
// would sail past the ceiling into an archive the Go uploader refuses outright,
// publishing nothing where stopping short would have published something.
test("stops rather than undercounting when the cache cannot be measured", () => {
  const dir = stubCompileCache();
  const { loads, load } = cachingLoader({});
  const dispatch = createDispatch({ entries: WARM_ENTRIES, primary: null, load });
  const readdir = vi.spyOn(fs, "readdirSync").mockImplementation(((target: string) => {
    if (target === dir) throw Object.assign(new Error("EACCES"), { code: "EACCES" });
    return [];
  }) as never);

  const report = dispatch.warm(NO_LIMITS);

  readdir.mockRestore();
  expect(report.stoppedBy).toBe("unmeasured");
  expect(loads).toEqual([]);
  expect(report.skippedCount).toBe(3);
});

// A directory node has not written yet is the one honest zero — the feature's
// normal state on the first entry of a cold instance.
test("measures a cache directory that does not exist yet as empty", () => {
  const dir = stubCompileCache();
  rmSync(dir, { recursive: true, force: true });
  const { load } = cachingLoader({});
  const dispatch = createDispatch({ entries: WARM_ENTRIES, primary: null, load });

  const report = dispatch.warm({ deadlineMs: Date.now() + 600_000, ceilingBytes: 64 << 20 });

  expect(report.stoppedBy).toBe("complete");
  expect(report.loaded).toBe(3);
});

// Next's own memoization, verbatim: route-module.ts builds the response cache
// from the first request to reach it and reuses it forever after.
const NEXT_REQUEST_META = Symbol.for("NextInternalRequestMeta");

function routeModuleLoader(getResponseCache?: () => never) {
  const caches: (boolean | undefined)[] = [];
  const routeModule = {
    responseCache: undefined as { minimal: boolean } | undefined,
    getResponseCache:
      getResponseCache ??
      ((req: any) => {
        if (!routeModule.responseCache) {
          const minimal = (req[NEXT_REQUEST_META] ?? {}).minimalMode ?? false;
          caches.push(minimal);
          routeModule.responseCache = { minimal };
        }
        return routeModule.responseCache;
      }),
  };
  return { caches, routeModule, load: () => ({ handler: () => {}, routeModule }) };
}

function resumeReq() {
  const req: any = fakeReq("app/page");
  req[NEXT_REQUEST_META] = { minimalMode: true };
  return req;
}

test("builds an entry's response cache non-minimal as it loads the entry", () => {
  const { caches, routeModule, load } = routeModuleLoader();

  createDispatch({ entries: ENTRIES, primary: "app/page", load });

  expect(caches).toEqual([false]);
  expect(routeModule.responseCache).toEqual({ minimal: false });
});

// The regression this pin exists for: a PPR resume runs in minimal mode, and is
// a likely first request, so an unpinned route module would memoize a minimal
// response cache — no durable ISR read or write — for the whole instance.
test("keeps that cache non-minimal when a minimal-mode request arrives first", () => {
  const { caches, routeModule, load } = routeModuleLoader();
  const dispatch = createDispatch({ entries: ENTRIES, primary: null, load });

  dispatch.handler(fakeReq("app/page"), fakeRes(), {});
  const cache = routeModule.getResponseCache(resumeReq());

  expect(cache).toEqual({ minimal: false });
  expect(caches).toEqual([false]);
});

test("still serves an entry whose response cache refuses to be built", () => {
  silenceErrors();
  const { load } = routeModuleLoader(() => {
    throw new Error("boom");
  });
  const dispatch = createDispatch({ entries: ENTRIES, primary: null, load });

  const res = fakeRes();
  dispatch.handler(fakeReq("app/page"), res, {});

  expect(res.statusCode).toBe(200);
});

// The pin's silent-failure mode: a Next that renames the getter takes the pin
// with it, and every instance a resume reaches first serves the rest of its life
// with ISR off. One line, once, is the only warning there would be.
test("reports a route module that exposes no response-cache getter, once", () => {
  const errors = silenceErrors();
  const load = () => ({ handler: () => {}, routeModule: {} });
  const dispatch = createDispatch({ entries: ENTRIES, primary: null, load });

  dispatch.handler(fakeReq("app/page"), fakeRes(), {});
  dispatch.handler(fakeReq("app/api/x/route"), fakeRes(), {});

  expect(errors).toHaveBeenCalledTimes(1);
  expect(String(errors.mock.calls[0]?.[0])).toMatch(/getResponseCache/);
});

// Node middleware (proxy.ts). The launcher injects it under this reserved key
// — see middlewareEntryKey in next-adapter.mts, mirrored as MIDDLEWARE_ENTRY_KEY
// in the source under test — never as an ordinary route.
const MIDDLEWARE_KEY = "/_middleware";

// A Writable stand-in for the real http.ServerResponse the membrane hands the
// dispatcher: middleware's response can carry a real body, so a plain object
// with just `.end()` can't be piped through node:stream/promises' pipeline.
function fakeMiddlewareRes() {
  const chunks: Buffer[] = [];
  const res = new Writable({
    write(chunk, _enc, cb) {
      chunks.push(Buffer.from(chunk));
      cb();
    },
  }) as Writable & {
    statusCode: number;
    headers: Record<string, unknown>;
    setHeader: (name: string, value: unknown) => void;
    text: () => string;
  };
  res.statusCode = 200;
  res.headers = {};
  res.setHeader = (name, value) => {
    res.headers[name.toLowerCase()] = value;
  };
  res.text = () => Buffer.concat(chunks).toString("utf8");
  return res;
}

// The worker always names middleware explicitly via x-ocel-entry — it is
// absent from the route table by design, so a self-fetch can never land on it
// by pathname (see next-adapter.mts's routeTable()).
function middlewareReq(overrides: Partial<http.IncomingMessage> = {}) {
  return {
    url: "/dashboard",
    method: "GET",
    headers: { host: "app.example.com", "x-ocel-entry": MIDDLEWARE_KEY },
    ...overrides,
  } as unknown as http.IncomingMessage;
}

test("loads the middleware entry at INIT alongside the primary", () => {
  const { loads, load } = fakeLoader();

  createDispatch({
    entries: { ...ENTRIES, [MIDDLEWARE_KEY]: "./.next/server/middleware.js" },
    primary: "app/page",
    load,
  });

  expect(loads).toEqual([
    ENTRIES["app/page"],
    "./.next/server/middleware.js",
  ]);
});

test("loads the middleware entry at INIT even with no primary to elect", () => {
  const { loads, load } = fakeLoader();

  createDispatch({
    entries: { [MIDDLEWARE_KEY]: "./.next/server/middleware.js" },
    primary: null,
    load,
  });

  expect(loads).toEqual(["./.next/server/middleware.js"]);
});

test("dispatches the reserved key through the adapter-function contract, not .handler", async () => {
  const seen: unknown[] = [];
  const response = new Response("hi", { status: 200 });
  const load = () => ({
    default: (args: unknown) => {
      seen.push(args);
      return { response };
    },
    handler: () => {
      throw new Error("must not be called for middleware");
    },
  });
  const dispatch = createDispatch({
    entries: { [MIDDLEWARE_KEY]: "./middleware.js" },
    primary: null,
    nextConfig: { basePath: "", i18n: null, trailingSlash: false, experimental: {} },
    load,
  });
  const res = fakeMiddlewareRes();

  await dispatch.handler(middlewareReq(), res, {});

  expect(res.statusCode).toBe(200);
  expect(res.text()).toBe("hi");
  expect(seen).toHaveLength(1);
  const call = seen[0] as { page: string; handler: unknown; request: Record<string, unknown> };
  expect(call.page).toBe("middleware");
  expect(call.request.method).toBe("GET");
  expect(call.request.url).toBe("https://app.example.com/dashboard");
  expect(call.request.nextConfig).toEqual({
    basePath: "",
    i18n: null,
    trailingSlash: false,
    experimental: {},
  });
  // GET carries no body: constructing a Request with one throws.
  expect(call.request.body).toBeUndefined();
});

test("prefers the forwarded host and proto the membrane normalizes onto the request", async () => {
  let seenUrl = "";
  const load = () => ({
    default: (args: { request: { url: string } }) => {
      seenUrl = args.request.url;
      return { response: new Response(null, { status: 200 }) };
    },
  });
  const dispatch = createDispatch({
    entries: { [MIDDLEWARE_KEY]: "./middleware.js" },
    primary: null,
    load,
  });

  await dispatch.handler(
    middlewareReq({
      url: "/blog/hello?x=1",
      headers: {
        host: "internal.local",
        "x-forwarded-host": "app.example.com",
        "x-forwarded-proto": "https",
        "x-ocel-entry": MIDDLEWARE_KEY,
      },
    } as never),
    fakeMiddlewareRes(),
    {},
  );

  expect(seenUrl).toBe("https://app.example.com/blog/hello?x=1");
});

test("carries a POST body as a readable web stream of the request's real bytes", async () => {
  const bodies: unknown[] = [];
  const load = () => ({
    default: async (args: { request: { body: ReadableStream } }) => {
      bodies.push(args.request.body);
      const chunks: Buffer[] = [];
      for await (const chunk of args.request.body) chunks.push(Buffer.from(chunk));
      return {
        response: new Response(Buffer.concat(chunks).toString("utf8"), { status: 200 }),
      };
    },
  });
  const dispatch = createDispatch({
    entries: { [MIDDLEWARE_KEY]: "./middleware.js" },
    primary: null,
    load,
  });

  // Readable.toWeb throws on a non-Uint8Array chunk, so this has to be real
  // Buffer chunks — a Readable.from(["string"]) source would throw the moment
  // anything actually consumed it, which a test that never reads the stream
  // would never notice.
  const { Readable } = await import("node:stream");
  const postReq = Readable.from([Buffer.from("pay"), Buffer.from("load")]) as unknown as http.IncomingMessage;
  Object.assign(postReq, {
    url: "/submit",
    method: "POST",
    headers: { host: "a", "x-ocel-entry": MIDDLEWARE_KEY },
  });
  const res = fakeMiddlewareRes();

  await dispatch.handler(postReq, res, {});

  expect(bodies[0]).toBeInstanceOf(ReadableStream);
  expect(res.text()).toBe("payload");
});

test("forwards every Set-Cookie value onto the real response", async () => {
  const response = new Response(null, { status: 200 });
  response.headers.append("set-cookie", "a=1; Path=/");
  response.headers.append("set-cookie", "b=2; Path=/");
  const load = () => ({ default: () => ({ response }) });
  const dispatch = createDispatch({
    entries: { [MIDDLEWARE_KEY]: "./middleware.js" },
    primary: null,
    load,
  });
  const res = fakeMiddlewareRes();

  await dispatch.handler(middlewareReq(), res, {});

  expect(res.headers["set-cookie"]).toEqual(["a=1; Path=/", "b=2; Path=/"]);
});

// A `proxy.ts` with a top-level await compiles to a module whose exports are
// a Promise, not the object itself — Next's own next-server.js documents and
// handles this. Without awaiting it, `.default` reads off the Promise object
// and is undefined, so every matched request 502s for the life of the
// deployment.
test("awaits a top-level-await middleware module before reading its default export", async () => {
  const load = () =>
    Promise.resolve({ default: () => ({ response: new Response("hi", { status: 200 }) }) });
  const dispatch = createDispatch({
    entries: { [MIDDLEWARE_KEY]: "./middleware.js" },
    primary: null,
    load,
  });
  const res = fakeMiddlewareRes();

  await dispatch.handler(middlewareReq(), res, {});

  expect(res.statusCode).toBe(200);
  expect(res.text()).toBe("hi");
});

// The failure is deterministic, so both requests see it — matching the
// memoized-failure semantics a synchronous require throw already gets.
test("fails closed on both requests when a top-level-await module rejects", async () => {
  silenceErrors();
  const load = () => Promise.reject(new Error("boom"));
  const dispatch = createDispatch({
    entries: { [MIDDLEWARE_KEY]: "./middleware.js" },
    primary: null,
    load,
  });

  const first = fakeMiddlewareRes();
  await dispatch.handler(middlewareReq(), first, {});
  const second = fakeMiddlewareRes();
  await dispatch.handler(middlewareReq(), second, {});

  expect(first.statusCode).toBe(502);
  expect(first.text()).toMatch(/boom/);
  expect(second.statusCode).toBe(502);
  expect(second.text()).toMatch(/boom/);
});

// The module is primed at INIT (loadEntry runs synchronously, kicking off the
// require's underlying work) even though nothing awaits its settlement until
// a request arrives — this is what proves priming does not regress under a
// Promise-valued module.
test("primes a top-level-await middleware module at INIT, not deferred to first request", () => {
  const loads: string[] = [];
  const load = (path: string) => {
    loads.push(path);
    return Promise.resolve({ default: () => ({}) });
  };

  createDispatch({
    entries: { [MIDDLEWARE_KEY]: "./middleware.js" },
    primary: null,
    load,
  });

  expect(loads).toEqual(["./middleware.js"]);
});

test("registers the adapter's waitUntil on the invocation's own ctx", async () => {
  let settled = false;
  const backgroundWork = Promise.resolve().then(() => {
    settled = true;
  });
  const load = () => ({
    default: () => ({ response: new Response(null, { status: 200 }), waitUntil: backgroundWork }),
  });
  const dispatch = createDispatch({
    entries: { [MIDDLEWARE_KEY]: "./middleware.js" },
    primary: null,
    load,
  });
  const registered: Promise<unknown>[] = [];
  const ctx = { waitUntil: (p: Promise<unknown>) => registered.push(p) };

  await dispatch.handler(middlewareReq(), fakeMiddlewareRes(), ctx);

  expect(registered).toEqual([backgroundWork]);
  await backgroundWork;
  expect(settled).toBe(true);
});

// Next's own next-server.js passes waitUntil inside the request object, not
// only as the return channel — a middleware that calls request.waitUntil()
// directly (rather than returning it) would otherwise register nothing, and
// the adapter's own NextFetchEvent fallback drops anything registered after
// the adapter returns instead of deferring it.
test("passes a request.waitUntil that forwards to the invocation's own ctx", async () => {
  let captured: ((p: Promise<unknown>) => void) | undefined;
  const registered: Promise<unknown>[] = [];
  const load = () => ({
    default: (args: { request: { waitUntil: (p: Promise<unknown>) => void } }) => {
      captured = args.request.waitUntil;
      return { response: new Response(null, { status: 200 }) };
    },
  });
  const dispatch = createDispatch({
    entries: { [MIDDLEWARE_KEY]: "./middleware.js" },
    primary: null,
    load,
  });
  const ctx = { waitUntil: (p: Promise<unknown>) => registered.push(p) };

  await dispatch.handler(middlewareReq(), fakeMiddlewareRes(), ctx);
  const work = Promise.resolve();
  captured?.(work);

  expect(registered).toEqual([work]);
});

test("fails closed when the middleware module throws", async () => {
  silenceErrors();
  const load = () => ({
    default: () => {
      throw new Error("boom");
    },
  });
  const dispatch = createDispatch({
    entries: { [MIDDLEWARE_KEY]: "./middleware.js" },
    primary: null,
    load,
  });
  const res = fakeMiddlewareRes();

  await dispatch.handler(middlewareReq(), res, {});

  expect(res.statusCode).toBe(502);
  expect(res.text()).toMatch(/boom/);
});

test("fails closed when the middleware module exports no adapter function", async () => {
  const load = () => ({ notAnAdapter: true });
  const dispatch = createDispatch({
    entries: { [MIDDLEWARE_KEY]: "./middleware.js" },
    primary: null,
    load,
  });
  const res = fakeMiddlewareRes();

  await dispatch.handler(middlewareReq(), res, {});

  expect(res.statusCode).toBe(502);
  expect(res.text()).toMatch(new RegExp(MIDDLEWARE_KEY.replace(/\//g, "\\/")));
});

test("fails closed when the adapter function returns no Response", async () => {
  const load = () => ({ default: () => ({ response: undefined }) });
  const dispatch = createDispatch({
    entries: { [MIDDLEWARE_KEY]: "./middleware.js" },
    primary: null,
    load,
  });
  const res = fakeMiddlewareRes();

  await dispatch.handler(middlewareReq(), res, {});

  expect(res.statusCode).toBe(502);
});

// warm() iterates every entry the bundle carries with no special case for
// middleware, so priming it at scale-out time falls out of the existing walk
// rather than needing its own path.
test("warm() picks up the middleware entry for free", () => {
  stubCompileCache();
  const { loads, load } = cachingLoader({});
  const dispatch = createDispatch({
    entries: { ...WARM_ENTRIES, [MIDDLEWARE_KEY]: "./middleware.js" },
    primary: "app/page",
    load,
  });

  const report = dispatch.warm(NO_LIMITS);

  expect(loads).toContain("./middleware.js");
  expect(report.entries).toBe(4);
  expect(report.loaded).toBe(4);
});
