import Module, { createRequire } from "node:module";
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
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
