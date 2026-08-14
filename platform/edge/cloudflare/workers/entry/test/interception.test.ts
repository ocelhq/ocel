import { tagSnapshotKey, type TagSnapshot } from "@framework/next-cache";
import { describe, expect, it } from "vitest";

import genesisSnapshot from "@framework/next-cache/fixtures/genesis-tag-snapshot.json?raw";
import {
  intercept,
  type InterceptDeps,
  type InterceptionConfig,
  type InterceptTarget,
} from "../src/interception";
import { createTagClock } from "../src/tag-clock";

const cfg: InterceptionConfig = { isrPrefix: "prod/proj/app/build" };

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

const snapshotKey = tagSnapshotKey(cfg.isrPrefix);

const snapshot = (over: Partial<TagSnapshot> = {}): TagSnapshot => ({
  version: 1,
  deployedAt: 500,
  generatedAt: 900,
  records: {},
  ...over,
});

const entryKey = (routePath: string) =>
  `${cfg.isrPrefix}/cache/${routePath === "/" ? "index" : routePath.replace(/^\//, "")}.cache.json`;

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
    expect(res!.headers.get("cache-control")).toBe("s-maxage=59");
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

  it("serves stale past the revalidate window, marked stale, within expiration", async () => {
    const store = stored({ [entryKey("/blog")]: appPage({ lastModified: 1_000 }) });
    const outcome = await intercept(req(), target({ revalidate: 60 }), cfg, {
      ...storeDeps(store),
      now: () => 1_000 + 61_000,
    });
    expect(outcome).toMatchObject({ kind: "complete", stale: true });
  });

  it("reports how much stale window a stale hit has left, and nothing when fresh", async () => {
    const store = stored({ [entryKey("/blog")]: appPage({ lastModified: 1_000 }) });
    const t = target({ revalidate: 60, expiration: 3600 });

    expect(
      await intercept(req(), t, cfg, {
        ...storeDeps(store),
        now: () => 1_000 + 3_599_500,
      }),
    ).toMatchObject({ stale: true, staleForMs: 500 });

    expect(
      await intercept(req(), t, cfg, {
        ...storeDeps(store),
        now: () => 1_000 + 1_000,
      }),
    ).toMatchObject({ stale: false, staleForMs: undefined });
  });

  it("takes its window from the entry when the manifest declares none", async () => {
    const store = stored({
      [entryKey("/blog")]: {
        ...appPage({ lastModified: 1_000 }),
        cacheControl: { revalidate: 1, expire: 31536000 },
      },
    });
    const outcome = await intercept(req(), target({ revalidate: undefined }), cfg, {
      ...storeDeps(store),
      now: () => 1_000 + 5_000,
    });
    expect(outcome).toMatchObject({ kind: "complete", stale: true });
  });

  it("prefers the entry's own window over the manifest's", async () => {
    const store = stored({
      [entryKey("/blog")]: {
        ...appPage({ lastModified: 1_000 }),
        cacheControl: { revalidate: 1, expire: 31536000 },
      },
    });
    const outcome = await intercept(req(), target({ revalidate: 3600 }), cfg, {
      ...storeDeps(store),
      now: () => 1_000 + 5_000,
    });
    expect(outcome).toMatchObject({ kind: "complete", stale: true });
  });

  it("treats an entry recorded as revalidate false as static", async () => {
    const store = stored({
      [entryKey("/blog")]: {
        ...appPage({ lastModified: 1_000 }),
        cacheControl: { revalidate: false, expire: undefined },
      },
    });
    const outcome = await intercept(req(), target({ revalidate: 60 }), cfg, {
      ...storeDeps(store),
      now: () => 1_000 + 10 * 365 * 86400_000,
    });
    expect(outcome).toMatchObject({ kind: "complete", stale: false });
  });

  it("fails open past the expiration window (hard cutoff)", async () => {
    const store = stored({ [entryKey("/blog")]: appPage({ lastModified: 1_000 }) });
    const res = await served(req(), target({ revalidate: 60, expiration: 3600 }), cfg, {
      ...storeDeps(store),
      now: () => 1_000 + 3_600_000,
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
  it("serves stale when a tag was marked stale after the entry", async () => {
    const store = stored({
      [entryKey("/blog")]: appPage({ tags: "products", lastModified: 1_000 }),
      [snapshotKey]: snapshot({
        records: { products: { stale: 1_500, expired: 9_000_000 } },
      }),
    });
    const deps = storeDeps(store, { now: () => 2_000 });

    const outcome = await intercept(req(), target(), cfg, deps);
    expect(outcome).toMatchObject({ kind: "complete", stale: true });
    expect(store.gets).toContain(snapshotKey);
  });

  it("serves nothing when a tag expired after the entry", async () => {
    const store = stored({
      [entryKey("/blog")]: appPage({ tags: "products", lastModified: 1_000 }),
      [snapshotKey]: snapshot({ records: { products: { expired: 1_500 } } }),
    });
    const deps = storeDeps(store, { now: () => 2_000 });

    expect(await intercept(req(), target(), cfg, deps)).toBeNull();
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

  const unusable: Record<string, string> = {
    missing: "",
    unparseable: "{not json",
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
      [snapshotKey]: snapshot({
        records: { products: { stale: 1_500, expired: 9_000_000 } },
      }),
    });
    const snapshotCache = fakeCache({ inert: true });

    expect(
      await served(req(), target(), cfg, storeDeps(store, { snapshotCache, now: () => 2_000 })),
    ).not.toBeNull();
    expect(
      await served(req(), target(), cfg, storeDeps(store, { snapshotCache, now: () => 20_000 })),
    ).not.toBeNull();

    expect(store.gets.filter((k) => k === snapshotKey).length).toBe(2);
  });

  it("keeps serving from a snapshot the PoP cache still holds", async () => {
    const store = stored({
      [entryKey("/blog")]: appPage({ tags: "products", lastModified: 1_000 }),
      [snapshotKey]: snapshot(),
    });
    const snapshotCache = fakeCache();

    expect(
      await served(req(), target(), cfg, storeDeps(store, { snapshotCache, now: () => 2_000 })),
    ).not.toBeNull();
    expect(
      await served(req(), target(), cfg, storeDeps(store, { snapshotCache, now: () => 11_000 })),
    ).not.toBeNull();
  });
});

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

  it("serves a PPR entry whose tags were marked stale, within expiration", async () => {
    const store = stored({
      [entryKey("/blog")]: pprEntry({ tags: "posts", lastModified: 1_000 }),
      [snapshotKey]: snapshot({
        records: { posts: { stale: 1_500, expired: 9_000_000 } },
      }),
    });
    expect(
      await intercept(req(), pprTarget(), cfg, storeDeps(store, { now: () => 2_000 })),
    ).toMatchObject({ kind: "ppr", stale: true });
  });

  it("falls open on a PPR entry whose tags expired after it was written", async () => {
    const store = stored({
      [entryKey("/blog")]: pprEntry({ tags: "posts", lastModified: 1_000 }),
      [snapshotKey]: snapshot({ records: { posts: { expired: 1_500 } } }),
    });
    expect(
      await intercept(req(), pprTarget(), cfg, storeDeps(store, { now: () => 2_000 })),
    ).toBeNull();
  });

  it("falls open when tags were marked stale past the expiration window", async () => {
    const store = stored({
      [entryKey("/blog")]: pprEntry({ tags: "posts", lastModified: 1_000 }),
      [snapshotKey]: snapshot({
        records: { posts: { stale: 1_500, expired: 9_000_000 } },
      }),
    });
    expect(
      await intercept(
        req(),
        pprTarget({ expiration: 3600 }),
        cfg,
        storeDeps(store, { now: () => 1_000 + 3_600_000 }),
      ),
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
    expect(res.headers.get("x-nextjs-postponed")).toBe("2");
    expect(res.headers.get("x-nextjs-stale-time")).toBe("300");
    expect(res.headers.get("vary")).toBe(
      "rsc, next-router-state-tree, next-router-prefetch, next-router-segment-prefetch",
    );
    expect(await res.text()).toBe("TREE-SEG");
  });

  const segmentReq = () =>
    req({
      headers: {
        RSC: "1",
        "next-router-prefetch": "1",
        "next-router-segment-prefetch": "/_tree",
      },
    });

  const readSegment = (
    t: InterceptTarget,
    entries: Record<string, unknown>,
    now: number,
  ) =>
    intercept(segmentReq(), t, cfg, storeDeps(stored(entries), { now: () => now }));

  const concreteTarget = () =>
    pprTarget({ routePath: "/posts/7", fallbackPath: "/posts/[id]" });

  it("answers a segment prefetch from the fallback when the concrete entry carries no segmentData", async () => {
    const outcome = await readSegment(
      concreteTarget(),
      {
        [entryKey("/posts/7")]: pprEntry(),
        [entryKey("/posts/[id]")]: pprEntry({
          segmentData: { "/_tree": btoa("FALLBACK-TREE") },
        }),
      },
      2_000,
    );

    expect(outcome?.kind).toBe("complete");
    const res = (outcome as { response: Response }).response;
    expect(res.status).toBe(200);
    expect(res.headers.get("x-nextjs-postponed")).toBe("2");
    expect(await res.text()).toBe("FALLBACK-TREE");
  });

  it("dates a segment served from the fallback by the fallback, not the concrete entry", async () => {
    const outcome = await readSegment(
      concreteTarget(),
      {
        [entryKey("/posts/7")]: pprEntry({ lastModified: 65_000 }),
        [entryKey("/posts/[id]")]: pprEntry({
          lastModified: 1_000,
          segmentData: { "/_tree": btoa("FALLBACK-TREE") },
        }),
      },
      70_000,
    );

    expect(outcome).toMatchObject({ lastModified: 1_000, stale: true });
  });

  it("still prefers the concrete entry when it carries the requested segment", async () => {
    const outcome = await readSegment(
      concreteTarget(),
      {
        [entryKey("/posts/7")]: pprEntry({
          segmentData: { "/_tree": btoa("CONCRETE-TREE") },
        }),
        [entryKey("/posts/[id]")]: pprEntry({
          segmentData: { "/_tree": btoa("FALLBACK-TREE") },
        }),
      },
      2_000,
    );

    const res = (outcome as { response: Response }).response;
    expect(await res.text()).toBe("CONCRETE-TREE");
  });

  it("serves nothing when neither the concrete entry nor the fallback holds the segment", async () => {
    const outcome = await readSegment(
      concreteTarget(),
      {
        [entryKey("/posts/7")]: pprEntry(),
        [entryKey("/posts/[id]")]: pprEntry({
          segmentData: { "/_head": btoa("HEAD") },
        }),
      },
      2_000,
    );

    expect(outcome).toBeNull();
  });

  it("marks a segment prefetch as a segment payload even when the build recorded no such header", async () => {
    const outcome = await intercept(
      req({ headers: { RSC: "1", "next-router-segment-prefetch": "/_tree" } }),
      pprTarget(),
      cfg,
      storeDeps(
        stored({
          [entryKey("/blog")]: pprEntry({
            segmentData: { "/_tree": btoa("TREE-SEG") },
            segmentHeaders: { "content-type": "text/x-component" },
          }),
        }),
        { now: () => 2_000 },
      ),
    );

    const res = (outcome as { response: Response }).response;
    expect(res.headers.get("x-nextjs-postponed")).toBe("2");
    expect(await res.text()).toBe("TREE-SEG");
  });

  it.each([
    ["fresh", 2_000, false],
    ["stale", 1_000 + 61_000, true],
  ])("reports a %s entry's staleness on a segment prefetch", async (_name, now, stale) => {
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
        { now: () => now },
      ),
    );

    expect(outcome?.kind).toBe("complete");
    expect(outcome?.stale).toBe(stale);
    expect(await (outcome as { response: Response }).response.text()).toBe("TREE-SEG");
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
    expect(res.headers.get("content-type")).toBe("text/x-component");
    expect(res.headers.get("x-nextjs-stale-time")).toBe("300");
    expect(res.headers.get("vary")).toBe(rscHeaders.vary);
    expect(res.headers.get("cache-control")).toMatch(/^s-maxage=\d+$/);
    expect(await res.text()).toBe("RSC-PAYLOAD");
  });

  it("stamps x-nextjs-postponed on a full-route prefetch of a postponed entry", async () => {
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
    expect(res.headers.get("x-nextjs-postponed")).toBe("1");
  });

  it("does not add x-nextjs-postponed to a full-route prefetch of a complete entry", async () => {
    const rscHeaders = {
      "content-type": "text/x-component",
      vary: "rsc, next-router-state-tree, next-router-prefetch, next-router-segment-prefetch",
      "x-nextjs-stale-time": "300",
    };
    const outcome = await intercept(
      req({ headers: { RSC: "1", "next-router-prefetch": "1" } }),
      target(),
      cfg,
      storeDeps(
        stored({ [entryKey("/blog")]: appPage({ rscHeaders }) }),
        { now: () => 2_000 },
      ),
    );

    expect(outcome?.kind).toBe("complete");
    const res = (outcome as { response: Response }).response;
    expect(res.headers.has("x-nextjs-postponed")).toBe(false);
  });

  it("does not add x-nextjs-postponed to the composed ppr response", async () => {
    const outcome = await read(
      pprTarget(),
      { [entryKey("/blog")]: pprEntry() },
      2_000,
    );

    expect(outcome?.kind).toBe("ppr");
    const shell = (outcome as { shell: Response }).shell;
    expect(shell.headers.has("x-nextjs-postponed")).toBe(false);
  });

  it("serves a segment prefetch even when the entry's tags were invalidated", async () => {
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

describe("intercept, a revalidated entry's segment prefetch", () => {
  const projection = {
    rscHeaders: { "content-type": "text/x-component" },
    segmentHeaders: SEGMENT_HEADERS,
  };

  const rewritten = (over: Record<string, unknown> = projection) => ({
    lastModified: 1_000,
    value: {
      kind: "APP_PAGE",
      status: 200,
      headers: { "content-type": "text/html; charset=utf-8" },
      html: "<html>fresh</html>",
      rscData: btoa("RSC-FRESH"),
      postponed: "STATE",
      segmentData: { "/_tree": btoa("TREE-FRESH") },
      ...over,
    },
  });

  const prefetch = (entry: unknown) =>
    intercept(
      req({ headers: { RSC: "1", "next-router-segment-prefetch": "/_tree" } }),
      target({ revalidate: 60, expiration: 3600 }),
      cfg,
      storeDeps(stored({ [entryKey("/blog")]: entry }), { now: () => 2_000 }),
    );

  it("reconstructs the segment from the reseeded variant headers", async () => {
    const outcome = await prefetch(rewritten());

    expect(outcome?.kind).toBe("complete");
    const res = (outcome as { response: Response }).response;
    expect(res.headers.get("x-nextjs-postponed")).toBe("2");
    expect(res.headers.get("content-type")).toBe("text/x-component");
    expect(await res.text()).toBe("TREE-FRESH");
  });

  it("serves nothing once the rewrite drops segmentHeaders", async () => {
    const { segmentHeaders: _dropped, ...withoutSegments } = projection;

    expect(await prefetch(rewritten(withoutSegments))).toBeNull();
  });
});

describe("intercept, entry memo", () => {
  it("collapses a burst on one entry to a single store read", async () => {
    const store = stored({ [entryKey("/blog")]: appPage({ lastModified: 1_000 }) });
    const deps = { store, now: () => 2_000 };

    await served(req(), target(), cfg, deps);
    await served(req(), target(), cfg, deps);
    await served(req(), target(), cfg, deps);

    expect(store.gets).toEqual([entryKey("/blog")]);
  });

  it("re-reads the store once the memo TTL has elapsed (no waitUntil)", async () => {
    const store = stored({ [entryKey("/blog")]: appPage({ lastModified: 1_000 }) });
    const clock = { ms: 2_000 };
    const deps = { store, now: () => clock.ms };

    await served(req(), target({ revalidate: false }), cfg, deps);
    clock.ms = 2_000 + 6_000; // past the 5s entry-memo TTL
    await served(req(), target({ revalidate: false }), cfg, deps);

    expect(store.gets).toEqual([entryKey("/blog"), entryKey("/blog")]);
  });

  it("serves the stale entry immediately and refreshes via waitUntil", async () => {
    const store = stored({ [entryKey("/blog")]: appPage({ lastModified: 1_000 }) });
    const clock = { ms: 2_000 };
    const pending: Promise<unknown>[] = [];
    const deps = {
      store,
      now: () => clock.ms,
      waitUntil: (p: Promise<unknown>) => pending.push(p),
    };

    await served(req(), target({ revalidate: false }), cfg, deps);
    expect(store.gets.length).toBe(1);

    clock.ms = 2_000 + 6_000; // stale
    await served(req(), target({ revalidate: false }), cfg, deps);
    expect(store.gets.length).toBe(1);

    await Promise.all(pending.splice(0));
    expect(store.gets.length).toBe(2);
  });
});

describe("intercept, entry-modified + parallel reads", () => {
  it("stamps the entry's lastModified on a complete response", async () => {
    const store = stored({ [entryKey("/blog")]: appPage({ lastModified: 1_000 }) });
    const res = await served(req(), target(), cfg, storeDeps(store, { now: () => 2_000 }));
    expect(res!.headers.get("x-ocel-entry-modified")).toBe("1000");
  });

  it("reads the entry and the tag snapshot concurrently, not in series", async () => {
    let releaseEntry: () => void;
    const entryGate = new Promise<void>((resolve) => {
      releaseEntry = resolve;
    });

    const objects: Record<string, string> = {
      [entryKey("/blog")]: JSON.stringify(appPage({ tags: "posts", lastModified: 1_000 })),
      [snapshotKey]: JSON.stringify(snapshot()),
    };
    const store = {
      gets: [] as string[],
      async get(key: string) {
        store.gets.push(key);
        if (key === entryKey("/blog")) await entryGate;
        const body = objects[key];
        return body === undefined ? null : { etag: `"${key}"`, text: async () => body };
      },
    };

    const result = served(
      req(),
      target({ revalidate: 60, tags: ["posts"] }),
      cfg,
      storeDeps(store, { now: () => 2_000 }),
    );

    for (let i = 0; i < 10; i++) await Promise.resolve();

    expect(store.gets).toContain(snapshotKey);
    expect(store.gets).toContain(entryKey("/blog"));
    expect(store.gets.indexOf(entryKey("/blog"))).toBeGreaterThanOrEqual(0);

    releaseEntry!();
    await result;

    expect(store.gets.filter((k) => k === snapshotKey).length).toBe(1);
  });
});

describe("priming the tag clock past the response", () => {
  it("hands the prime to waitUntil when the target declares tags", async () => {
    const store = stored({
      [entryKey("/blog")]: appPage({ tags: "products", lastModified: 1_000 }),
      [snapshotKey]: snapshot(),
    });
    const pending: Promise<unknown>[] = [];
    await served(req(), target({ tags: ["products"] }), cfg, {
      ...storeDeps(store),
      now: () => 2_000,
      waitUntil: (p) => pending.push(p),
    });

    expect(pending).toHaveLength(1);
    await expect(pending[0]).resolves.not.toThrow();
  });

  it("does not prime at all for a target with no tags", async () => {
    const store = stored({ [entryKey("/blog")]: appPage({ lastModified: 1_000 }) });
    const pending: Promise<unknown>[] = [];
    await served(req(), target(), cfg, {
      ...storeDeps(store),
      now: () => 2_000,
      waitUntil: (p) => pending.push(p),
    });

    expect(pending).toHaveLength(0);
    expect(store.gets).not.toContain(snapshotKey);
  });

  it("recovers from an orphaned prime so a later request does not hang forever", async () => {
    let snapshotGets = 0;
    const store = {
      gets: [] as string[],
      async get(key: string) {
        this.gets.push(key);
        if (key === snapshotKey) {
          snapshotGets++;
          if (snapshotGets === 1) return new Promise<never>(() => {});
          return { etag: `"${key}"`, text: async () => JSON.stringify(snapshot()) };
        }
        const objects: Record<string, string> = {
          [entryKey("/blog")]: JSON.stringify(
            appPage({ tags: "products", lastModified: 1_000 }),
          ),
        };
        const body = objects[key];
        return body === undefined ? null : { etag: `"${key}"`, text: async () => body };
      },
    };
    const tagClock = createTagClock(cfg, { store, snapshotReadTimeoutMs: 5 });
    const deps: InterceptDeps = { store, now: () => 2_000, tagClock };

    await intercept(
      req({ headers: { "next-router-segment-prefetch": "/_tree" } }),
      target({ tags: ["products"] }),
      cfg,
      deps,
    );
    expect(snapshotGets).toBe(1);

    const stuck = await intercept(req(), target(), cfg, deps);
    expect(stuck).toBeNull();
    expect(snapshotGets).toBe(1);

    const recovered = await intercept(req(), target(), cfg, deps);
    expect(recovered).not.toBeNull();
    expect(snapshotGets).toBe(2);
  });
});

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

  it("stays trusted however long the build goes without an invalidation", async () => {
    const store = fakeStore({
      [entryKey("/blog")]: JSON.stringify(
        appPage({ tags: "products", lastModified: genesis.deployedAt }),
      ),
      [snapshotKey]: genesisSnapshot,
    });
    const res = await served(req(), target({ revalidate: false }), cfg, {
      ...storeDeps(store),
      now: () => genesis.deployedAt + 30 * 86_400_000,
    });
    expect(res).not.toBeNull();
  });

  it("is addressed at the key the publisher writes it to", () => {
    expect(snapshotKey).toBe("prod/proj/app/build/tag-clock.json");
  });
});
