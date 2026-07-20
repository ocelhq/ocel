import { tagSnapshotKey, type TagSnapshot } from "@ocel/next-cache";
import { describe, expect, it } from "vitest";

import genesisSnapshot from "../../../packages/next-cache/fixtures/genesis-tag-snapshot.json?raw";
import {
  intercept,
  type InterceptDeps,
  type InterceptionConfig,
  type InterceptTarget,
} from "../src/interception";

const cfg: InterceptionConfig = { prefix: "prod/proj/app/build" };

// A fake object-store binding, shaped like the R2 bucket the deploy binds as
// OCEL_CACHE_STORE: canned bodies by key, every get recorded so a test can
// assert both what was read and that nothing else was. `fail` models a store
// error, which must fall open exactly like a miss.
function fakeStore(objects: Record<string, string>, opts: { fail?: boolean } = {}) {
  const gets: string[] = [];
  return {
    gets,
    async get(key: string) {
      gets.push(key);
      if (opts.fail) throw new Error("store unavailable");
      const body = objects[key];
      if (body === undefined) return null;
      return { etag: `"${key}"`, text: async () => body };
    },
  };
}

// Builds a store from entry values, JSON-encoding each the way the build writes
// them, so tests can hand over plain objects.
function stored(
  entries: Record<string, unknown>,
  opts: { fail?: boolean } = {},
) {
  const objects: Record<string, string> = {};
  for (const [key, value] of Object.entries(entries)) {
    objects[key] = JSON.stringify(value);
  }
  return fakeStore(objects, opts);
}

// A Map-backed stand-in for caches.default. `inert` models *.workers.dev, where
// put() is silently discarded and match() never hits, so the snapshot read has
// to degrade to a direct store GET.
function fakeCache(opts: { inert?: boolean } = {}) {
  const store = new Map<string, string>();
  const calls = { match: 0, put: 0 };
  return {
    calls,
    async match(request: Request): Promise<Response | undefined> {
      calls.match++;
      const body = store.get(request.url);
      return body === undefined ? undefined : new Response(body);
    },
    async put(request: Request, response: Response): Promise<void> {
      calls.put++;
      const body = await response.text();
      if (!opts.inert) store.set(request.url, body);
    },
  };
}

const snapshotKey = tagSnapshotKey(cfg.prefix);

const snapshot = (over: Partial<TagSnapshot> = {}): TagSnapshot => ({
  version: 1,
  deployedAt: 500,
  generatedAt: 900,
  validUntil: 300_900,
  records: {},
  ...over,
});

const entryKey = (routePath: string) =>
  `${cfg.prefix}/cache/${routePath === "/" ? "index" : routePath.replace(/^\//, "")}.cache.json`;

// The build seeds identical segment headers across a group; the marker the
// client gates PPR on is x-nextjs-postponed: 2. Tests default to it whenever they
// supply segmentData, mirroring what emitCacheEntries writes.
const SEGMENT_HEADERS = {
  "content-type": "text/x-component",
  vary: "rsc, next-router-state-tree, next-router-prefetch, next-router-segment-prefetch",
  "x-nextjs-stale-time": "300",
  "x-nextjs-postponed": "2",
};

function appPage(
  opts: {
    tags?: string;
    lastModified?: number;
    postponed?: unknown;
    segmentData?: Record<string, string>;
    segmentHeaders?: Record<string, string> | null;
    rscHeaders?: Record<string, string>;
  } = {},
) {
  const segmentHeaders =
    opts.segmentHeaders === null
      ? undefined
      : (opts.segmentHeaders ?? (opts.segmentData ? SEGMENT_HEADERS : undefined));
  return {
    lastModified: opts.lastModified ?? 1_000,
    value: {
      kind: "APP_PAGE",
      html: "<html>hi</html>",
      rscData: btoa("RSC-PAYLOAD"),
      status: 200,
      headers: opts.tags ? { "x-next-cache-tags": opts.tags } : {},
      ...(opts.rscHeaders ? { rscHeaders: opts.rscHeaders } : {}),
      ...(segmentHeaders ? { segmentHeaders } : {}),
      ...(opts.postponed !== undefined ? { postponed: opts.postponed } : {}),
      ...(opts.segmentData ? { segmentData: opts.segmentData } : {}),
    },
  };
}

const storeDeps = (
  store: ReturnType<typeof fakeStore>,
  over: Partial<InterceptDeps> = {},
): InterceptDeps => ({ store, ...over });

// Most cases here are about whether an entry serves at all, which is the
// complete-entry contract; `served` narrows to that so they read plainly. The
// PPR suite calls intercept directly.
async function served(
  ...args: Parameters<typeof intercept>
): Promise<Response | null> {
  const outcome = await intercept(...args);
  return outcome?.kind === "complete" ? outcome.response : null;
}

const req = (init?: RequestInit) => new Request("https://app.example/blog", init);
const target = (over: Partial<InterceptTarget> = {}): InterceptTarget => ({
  routePath: "/blog",
  revalidate: 60,
  ...over,
});

describe("intercept, complete entries", () => {
  it("serves html for a fresh untagged page, reading only its one object", async () => {
    const store = stored({ [entryKey("/blog")]: appPage() });
    const res = await served(req(), target(), cfg, storeDeps(store, { now: () => 2_000 }));

    expect(res).not.toBeNull();
    expect(res!.status).toBe(200);
    expect(res!.headers.get("content-type")).toBe("text/html; charset=utf-8");
    expect(await res!.text()).toBe("<html>hi</html>");
    // Entry is 1s old (lastModified 1_000, now 2_000), so the CDN gets the
    // remaining window, not the full 60s.
    expect(res!.headers.get("cache-control")).toBe("s-maxage=59");
    // Untagged, so the snapshot is never read.
    expect(store.gets).toEqual([entryKey("/blog")]);
  });

  it("serves the RSC payload when the request negotiates RSC", async () => {
    const store = stored({ [entryKey("/blog")]: appPage() });
    const res = await served(
      req({ headers: { RSC: "1" } }),
      target(),
      cfg,
      storeDeps(store, { now: () => 2_000 }),
    );

    expect(res!.headers.get("content-type")).toBe("text/x-component");
    expect(await res!.text()).toBe("RSC-PAYLOAD");
  });

  it("fails open (null) on a store miss", async () => {
    const store = stored({});
    expect(await served(req(), target(), cfg, storeDeps(store, { now: () => 2_000 }))).toBeNull();
  });

  it("fails open when the store errors", async () => {
    const store = stored({}, { fail: true });
    expect(await served(req(), target(), cfg, storeDeps(store, { now: () => 2_000 }))).toBeNull();
  });

  it("fails open past the revalidate window (time-based expiry)", async () => {
    const store = stored({ [entryKey("/blog")]: appPage({ lastModified: 1_000 }) });
    // 61s later, revalidate is 60s.
    const res = await served(req(), target({ revalidate: 60 }), cfg, {
      ...storeDeps(store),
      now: () => 1_000 + 61_000,
    });
    expect(res).toBeNull();
  });

  it("stays fresh within the window with a false (static) revalidate", async () => {
    const store = stored({ [entryKey("/blog")]: appPage({ lastModified: 1_000 }) });
    const res = await served(req(), target({ revalidate: false }), cfg, {
      ...storeDeps(store),
      now: () => 1_000 + 10 * 365 * 86400_000,
    });
    expect(res).not.toBeNull();
    expect(res!.headers.get("cache-control")).toBe("s-maxage=31536000");
  });

  it("serves an APP_ROUTE body with its stored headers verbatim", async () => {
    const entry = {
      lastModified: 1_000,
      value: {
        kind: "APP_ROUTE",
        body: btoa('{"ok":true}'),
        status: 201,
        headers: { "content-type": "application/json" },
      },
    };
    const store = stored({ [entryKey("/blog")]: entry });
    const res = await served(req(), target(), cfg, storeDeps(store, { now: () => 2_000 }));
    expect(res!.status).toBe(201);
    expect(res!.headers.get("content-type")).toBe("application/json");
    expect(await res!.text()).toBe('{"ok":true}');
  });

  it("returns a PPR shell — not a complete response — for a postponed page", async () => {
    const store = stored({ [entryKey("/blog")]: appPage({ postponed: "STATE" }) });
    const outcome = await intercept(req(), target(), cfg, storeDeps(store, { now: () => 2_000 }));
    expect(outcome?.kind).toBe("ppr");
  });
});

describe("intercept, tag state from the snapshot", () => {
  it("fails open when a tag was revalidated after the entry", async () => {
    const store = stored({
      [entryKey("/blog")]: appPage({ tags: "products", lastModified: 1_000 }),
      [snapshotKey]: snapshot({ records: { products: { expired: 1_500 } } }),
    });
    const deps = storeDeps(store, { now: () => 2_000 });

    expect(await served(req(), target(), cfg, deps)).toBeNull();
    expect(store.gets).toContain(snapshotKey);
  });

  it("serves a tagged page whose tag expired before the entry was written", async () => {
    const store = stored({
      [entryKey("/blog")]: appPage({ tags: "products", lastModified: 1_000 }),
      [snapshotKey]: snapshot({ records: { products: { expired: 500 } } }),
    });
    const res = await served(req(), target(), cfg, storeDeps(store, { now: () => 2_000 }));
    expect(res).not.toBeNull();
  });

  it("serves a tagged page the snapshot has no record for", async () => {
    const store = stored({
      [entryKey("/blog")]: appPage({ tags: "products", lastModified: 1_000 }),
      [snapshotKey]: snapshot(),
    });
    const res = await served(req(), target(), cfg, storeDeps(store, { now: () => 2_000 }));
    expect(res).not.toBeNull();
  });

  // Every unusable snapshot is a fall-open rather than a serve: waking the
  // Lambda is what republishes it, so the liveness loop repairs itself.
  const unusable: Record<string, string> = {
    missing: "",
    unparseable: "{not json",
    "past validUntil": JSON.stringify(snapshot({ validUntil: 1_999 })),
    "a version this worker does not know": JSON.stringify({
      ...snapshot({ records: { products: { expired: 500 } } }),
      version: 2,
    }),
  };

  for (const [why, body] of Object.entries(unusable)) {
    it(`falls open on a snapshot that is ${why}`, async () => {
      const store = fakeStore({
        [entryKey("/blog")]: JSON.stringify(
          appPage({ tags: "products", lastModified: 1_000 }),
        ),
        ...(body ? { [snapshotKey]: body } : {}),
      });
      expect(
        await served(req(), target(), cfg, storeDeps(store, { now: () => 2_000 })),
      ).toBeNull();
    });
  }

  it("reads the snapshot once across requests when the PoP cache works", async () => {
    const store = stored({
      [entryKey("/blog")]: appPage({ tags: "products", lastModified: 1_000 }),
      [snapshotKey]: snapshot(),
    });
    const snapshotCache = fakeCache();

    // Far enough apart that the in-isolate memo has lapsed, so the second read
    // is answered by the PoP cache rather than the memo.
    expect(
      await served(req(), target(), cfg, storeDeps(store, { snapshotCache, now: () => 2_000 })),
    ).not.toBeNull();
    expect(
      await served(req(), target(), cfg, storeDeps(store, { snapshotCache, now: () => 20_000 })),
    ).not.toBeNull();

    expect(store.gets.filter((k) => k === snapshotKey).length).toBe(1);
    expect(snapshotCache.calls.put).toBe(1);
    expect(snapshotCache.calls.match).toBe(2);
  });

  it("serves a burst from the in-isolate memo without touching the PoP cache", async () => {
    const store = stored({
      [entryKey("/blog")]: appPage({ tags: "products", lastModified: 1_000 }),
      [snapshotKey]: snapshot(),
    });
    const snapshotCache = fakeCache();

    await served(req(), target(), cfg, storeDeps(store, { snapshotCache, now: () => 2_000 }));
    await served(req(), target(), cfg, storeDeps(store, { snapshotCache, now: () => 2_100 }));

    expect(snapshotCache.calls.match).toBe(1);
    expect(store.gets.filter((k) => k === snapshotKey).length).toBe(1);
  });

  it("stays correct with an inert PoP cache, paying a store read per request", async () => {
    const store = stored({
      [entryKey("/blog")]: appPage({ tags: "products", lastModified: 1_000 }),
      [snapshotKey]: snapshot({ records: { products: { expired: 1_500 } } }),
    });
    const snapshotCache = fakeCache({ inert: true });

    expect(
      await served(req(), target(), cfg, storeDeps(store, { snapshotCache, now: () => 2_000 })),
    ).toBeNull();
    expect(
      await served(req(), target(), cfg, storeDeps(store, { snapshotCache, now: () => 20_000 })),
    ).toBeNull();

    expect(store.gets.filter((k) => k === snapshotKey).length).toBe(2);
  });

  it("falls open rather than trusting a snapshot the PoP cache held past validUntil", async () => {
    const store = stored({
      [entryKey("/blog")]: appPage({ tags: "products", lastModified: 1_000 }),
      [snapshotKey]: snapshot({ validUntil: 10_000 }),
    });
    const snapshotCache = fakeCache();

    expect(
      await served(req(), target(), cfg, storeDeps(store, { snapshotCache, now: () => 2_000 })),
    ).not.toBeNull();
    expect(
      await served(req(), target(), cfg, storeDeps(store, { snapshotCache, now: () => 11_000 })),
    ).toBeNull();
  });
});

// A PPR entry is discriminated on value.postponed, never on renderingMode: the
// build marks routes PARTIALLY_STATIC whether or not they actually postponed,
// and revalidation can change the answer under a fixed manifest.
describe("intercept, PPR entries", () => {
  const pprEntry = (over: Parameters<typeof appPage>[0] = {}) =>
    appPage({ postponed: "STATE", ...over });

  const pprTarget = (over: Partial<InterceptTarget> = {}) =>
    target({ revalidate: 60, expiration: 3600, ...over });

  const read = (
    t: InterceptTarget,
    entries: Record<string, unknown>,
    now: number,
  ) => intercept(req(), t, cfg, storeDeps(stored(entries), { now: () => now }));

  it("hands back the shell, the postponed state, and no shared-cache claim", async () => {
    const outcome = await read(pprTarget(), { [entryKey("/blog")]: pprEntry() }, 2_000);

    expect(outcome).toMatchObject({ kind: "ppr", postponed: "STATE", stale: false });
    const shell = (outcome as { shell: Response }).shell;
    expect(await shell.text()).toBe("<html>hi</html>");
    // Freshness is the caller's to declare; nothing here invites a cache.
    expect(shell.headers.get("cache-control")).toBeNull();
  });

  it("serves the shell for the RSC variant off the same postponed state", async () => {
    const outcome = await intercept(
      req({ headers: { RSC: "1" } }),
      pprTarget(),
      cfg,
      storeDeps(stored({ [entryKey("/blog")]: pprEntry() }), { now: () => 2_000 }),
    );

    expect(outcome).toMatchObject({ kind: "ppr", postponed: "STATE" });
    const shell = (outcome as { shell: Response }).shell;
    expect(shell.headers.get("content-type")).toBe("text/x-component");
    expect(await shell.text()).toBe("RSC-PAYLOAD");
  });

  it("still serves past initialRevalidate, marked stale", async () => {
    const outcome = await read(
      pprTarget(),
      { [entryKey("/blog")]: pprEntry({ lastModified: 1_000 }) },
      1_000 + 61_000,
    );

    expect(outcome).toMatchObject({ kind: "ppr", stale: true });
  });

  it("falls open past initialExpiration", async () => {
    const outcome = await read(
      pprTarget({ expiration: 3600 }),
      { [entryKey("/blog")]: pprEntry({ lastModified: 1_000 }) },
      1_000 + 3_600_000,
    );

    expect(outcome).toBeNull();
  });

  it("still refuses a PPR entry whose tags were invalidated", async () => {
    const store = stored({
      [entryKey("/blog")]: pprEntry({ tags: "posts", lastModified: 1_000 }),
      [snapshotKey]: snapshot({ records: { posts: { expired: 1_500 } } }),
    });
    expect(
      await intercept(req(), pprTarget(), cfg, storeDeps(store, { now: () => 2_000 })),
    ).toBeNull();
  });

  it("resumes a concrete path from the route's param-agnostic fallback shell", async () => {
    const outcome = await read(
      pprTarget({ routePath: "/posts/7", fallbackPath: "/posts/[id]" }),
      { [entryKey("/posts/[id]")]: pprEntry() },
      2_000,
    );

    expect(outcome).toMatchObject({ kind: "ppr", postponed: "STATE" });
  });

  it("never serves a complete entry found under the dynamic pattern", async () => {
    // Without a postponed state that entry is some other path's rendered page.
    const outcome = await read(
      pprTarget({ routePath: "/posts/7", fallbackPath: "/posts/[id]" }),
      { [entryKey("/posts/[id]")]: appPage() },
      2_000,
    );

    expect(outcome).toBeNull();
  });

  it("prefers the concrete entry over the fallback shell", async () => {
    const outcome = await read(
      pprTarget({ routePath: "/posts/7", fallbackPath: "/posts/[id]" }),
      {
        [entryKey("/posts/7")]: appPage(),
        [entryKey("/posts/[id]")]: pprEntry(),
      },
      2_000,
    );

    expect(outcome?.kind).toBe("complete");
  });

  it("serves a segment prefetch from segmentData, not the composed shell", async () => {
    const outcome = await intercept(
      req({
        headers: {
          RSC: "1",
          "next-router-prefetch": "1",
          "next-router-segment-prefetch": "/_tree",
        },
      }),
      pprTarget(),
      cfg,
      storeDeps(
        stored({
          [entryKey("/blog")]: pprEntry({
            segmentData: { "/_tree": btoa("TREE-SEG") },
          }),
        }),
        { now: () => 2_000 },
      ),
    );

    expect(outcome?.kind).toBe("complete");
    const res = (outcome as { response: Response }).response;
    expect(res.status).toBe(200);
    expect(res.headers.get("content-type")).toBe("text/x-component");
    // The marker the client gates PPR support on — its absence is what silently
    // degrades a PPR route to a whole-page dynamic render.
    expect(res.headers.get("x-nextjs-postponed")).toBe("2");
    expect(res.headers.get("x-nextjs-stale-time")).toBe("300");
    expect(res.headers.get("vary")).toBe(
      "rsc, next-router-state-tree, next-router-prefetch, next-router-segment-prefetch",
    );
    expect(await res.text()).toBe("TREE-SEG");
  });

  it("strips the internal tag header from a segment response", async () => {
    const outcome = await intercept(
      req({ headers: { "next-router-segment-prefetch": "/_tree" } }),
      pprTarget(),
      cfg,
      storeDeps(
        stored({
          [entryKey("/blog")]: pprEntry({
            segmentData: { "/_tree": btoa("TREE-SEG") },
            segmentHeaders: { ...SEGMENT_HEADERS, "x-next-cache-tags": "posts" },
          }),
        }),
        { now: () => 2_000 },
      ),
    );

    const res = (outcome as { response: Response }).response;
    expect(res.headers.get("x-next-cache-tags")).toBeNull();
    expect(res.headers.get("x-nextjs-postponed")).toBe("2");
  });

  it("falls open on a segment prefetch when the entry predates header capture", async () => {
    // An entry without segmentHeaders can only serve a segment missing the
    // postponed marker, which the client reads as "not PPR" — worse than a miss.
    const outcome = await intercept(
      req({ headers: { "next-router-segment-prefetch": "/_tree" } }),
      pprTarget(),
      cfg,
      storeDeps(
        stored({
          [entryKey("/blog")]: pprEntry({
            segmentData: { "/_tree": btoa("TREE-SEG") },
            segmentHeaders: null,
          }),
        }),
        { now: () => 2_000 },
      ),
    );

    expect(outcome).toBeNull();
  });

  it("serves a full-route prefetch as the cacheable shell, not a resume", async () => {
    // A router prefetch (no segment header) must get the static shell as a
    // complete, cacheable response — never a PPR pair that would resume a
    // per-visitor render the client cannot cache.
    const rscHeaders = {
      "content-type": "text/x-component",
      vary: "rsc, next-router-state-tree, next-router-prefetch, next-router-segment-prefetch",
      "x-nextjs-stale-time": "300",
    };
    const outcome = await intercept(
      req({ headers: { RSC: "1", "next-router-prefetch": "1" } }),
      pprTarget(),
      cfg,
      storeDeps(
        stored({ [entryKey("/blog")]: pprEntry({ rscHeaders }) }),
        { now: () => 2_000 },
      ),
    );

    expect(outcome?.kind).toBe("complete");
    const res = (outcome as { response: Response }).response;
    // The RSC variant's own stored headers are replayed verbatim.
    expect(res.headers.get("content-type")).toBe("text/x-component");
    expect(res.headers.get("x-nextjs-stale-time")).toBe("300");
    expect(res.headers.get("vary")).toBe(rscHeaders.vary);
    expect(res.headers.get("cache-control")).toMatch(/^s-maxage=\d+$/);
    expect(await res.text()).toBe("RSC-PAYLOAD");
  });

  it("serves a segment prefetch even when the entry's tags were invalidated", async () => {
    // A prefetch is speculative: its result is revealed only on a later
    // navigation, which resumes the tagged half fresh — so the prefetch carries
    // no tagged content to be stale. An invalidated tag must not strand it on
    // the Lambda, which would starve the client's segment cache. The tag gate is
    // bypassed entirely, so the snapshot is never even read.
    const store = stored({
      [entryKey("/blog")]: pprEntry({
        tags: "posts",
        lastModified: 1_000,
        segmentData: { "/_tree": btoa("TREE-SEG") },
      }),
      [snapshotKey]: snapshot({ records: { posts: { expired: 1_500 } } }),
    });
    const outcome = await intercept(
      req({
        headers: {
          RSC: "1",
          "next-router-prefetch": "1",
          "next-router-segment-prefetch": "/_tree",
        },
      }),
      pprTarget(),
      cfg,
      storeDeps(store, { now: () => 2_000 }),
    );

    expect(outcome?.kind).toBe("complete");
    const res = (outcome as { response: Response }).response;
    expect(await res.text()).toBe("TREE-SEG");
    expect(store.gets).not.toContain(snapshotKey);
  });

  it("serves a full-route prefetch even when the entry's tags were invalidated", async () => {
    const store = stored({
      [entryKey("/blog")]: pprEntry({ tags: "posts", lastModified: 1_000 }),
      [snapshotKey]: snapshot({ records: { posts: { expired: 1_500 } } }),
    });
    const outcome = await intercept(
      req({ headers: { RSC: "1", "next-router-prefetch": "1" } }),
      pprTarget(),
      cfg,
      storeDeps(store, { now: () => 2_000 }),
    );

    expect(outcome?.kind).toBe("complete");
    const res = (outcome as { response: Response }).response;
    expect(await res.text()).toBe("RSC-PAYLOAD");
    expect(store.gets).not.toContain(snapshotKey);
  });

  it("falls open when the requested segment is absent from the entry", async () => {
    const outcome = await intercept(
      req({ headers: { "next-router-segment-prefetch": "/_missing" } }),
      pprTarget(),
      cfg,
      storeDeps(
        stored({
          [entryKey("/blog")]: pprEntry({
            segmentData: { "/_tree": btoa("TREE-SEG") },
          }),
        }),
        { now: () => 2_000 },
      ),
    );

    expect(outcome).toBeNull();
  });

  describe("intercept, runtime prefetch values", () => {
    it("serves the static shell for next-router-prefetch: 1", async () => {
      const store = stored({
        [entryKey("/blog")]: appPage({ postponed: "PP" }),
      });
      const outcome = await intercept(
        req({ headers: { RSC: "1", "next-router-prefetch": "1" } }),
        target(),
        cfg,
        storeDeps(store, { now: () => 2_000 }),
      );
      expect(outcome?.kind).toBe("complete");
    });

    it("falls open for a runtime prefetch (next-router-prefetch: 2)", async () => {
      const store = stored({
        [entryKey("/blog")]: appPage({ postponed: "PP" }),
      });
      // A '2' prefetch is a runtime dynamic request; it must NOT be handed the
      // static shell, so with no other servable variant the read falls open.
      const outcome = await intercept(
        req({ headers: { RSC: "1", "next-router-prefetch": "2" } }),
        target({ revalidate: false }),
        cfg,
        storeDeps(store, { now: () => 2_000 }),
      );
      expect(outcome).toBeNull();
    });
  });
});

// The snapshot format crosses a language boundary twice — the Go deploy seeds it
// and the Lambda publisher rewrites it — so nothing but this one artifact stops
// the three from drifting apart in silence. The deploy's test asserts it marshals
// exactly these bytes and the publisher's test asserts it reads these fields;
// what follows is the reader half, run against the same bytes rather than
// against a snapshot this test wrote for itself.
describe("the published snapshot format", () => {
  const genesis: TagSnapshot = JSON.parse(genesisSnapshot);
  const within = genesis.generatedAt + 1_000;

  it("is served, verbatim, by a worker reading it out of the store", async () => {
    const store = fakeStore({
      [entryKey("/blog")]: JSON.stringify(
        appPage({ tags: "products", lastModified: genesis.deployedAt }),
      ),
      [snapshotKey]: genesisSnapshot,
    });

    const res = await served(
      req(),
      target({ revalidate: false }),
      cfg,
      storeDeps(store, { now: () => within }),
    );
    expect(res).not.toBeNull();
  });

  it("stops being trusted at the validity window the publisher declared", async () => {
    const store = fakeStore({
      [entryKey("/blog")]: JSON.stringify(
        appPage({ tags: "products", lastModified: genesis.deployedAt }),
      ),
      [snapshotKey]: genesisSnapshot,
    });
    const res = await served(req(), target({ revalidate: false }), cfg, {
      ...storeDeps(store),
      now: () => genesis.validUntil,
    });
    expect(res).toBeNull();
  });

  it("is addressed at the key the publisher writes it to", () => {
    expect(snapshotKey).toBe("prod/proj/app/build/tag-clock.json");
  });
});
