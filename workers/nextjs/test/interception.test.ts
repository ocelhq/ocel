import { tagSnapshotKey, type TagSnapshot } from "@ocel/next-cache";
import { describe, expect, it } from "vitest";

import genesisSnapshot from "../../../packages/next-cache/fixtures/genesis-tag-snapshot.json?raw";
import {
  intercept,
  readInterceptionConfig,
  type InterceptDeps,
  type InterceptionConfig,
  type InterceptTarget,
} from "../src/interception";

const cfg: InterceptionConfig = {
  accessKeyId: "AKIA",
  secretKey: "secret",
  region: "us-east-1",
  bucket: "assets",
  table: "state",
  prefix: "prod/proj/app/build",
  tagNamespace: "TAG#prod#proj#app#build#",
};

// A fake AWS signer: routes by host, serving one canned S3 entry and canned DDB
// tag records, and recording the calls so a test can assert what interception
// read. Keys are the S3 object keys (without the .cache.json suffix stripped).
function fakeAws(opts: {
  entries?: Record<string, unknown>;
  tags?: Record<string, { expired?: number; stale?: number }>;
  s3Status?: number;
  ddbStatus?: number;
}): InterceptDeps & { s3Calls: string[]; ddbCalls: unknown[] } {
  const s3Calls: string[] = [];
  const ddbCalls: unknown[] = [];
  const signedFetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = typeof input === "string" ? input : input.toString();
    if (url.includes(".s3.")) {
      const key = decodeURIComponent(new URL(url).pathname.slice(1));
      s3Calls.push(key);
      if (opts.s3Status && opts.s3Status !== 200) {
        return new Response("", { status: opts.s3Status });
      }
      const entry = opts.entries?.[key];
      if (!entry) return new Response("", { status: 404 });
      return new Response(JSON.stringify(entry), { status: 200 });
    }
    // DynamoDB BatchGetItem.
    const body = JSON.parse(String(init?.body));
    ddbCalls.push(body);
    if (opts.ddbStatus && opts.ddbStatus !== 200) {
      return new Response("", { status: opts.ddbStatus });
    }
    const keys = body.RequestItems[cfg.table].Keys as { pk: { S: string } }[];
    const items = [];
    for (const k of keys) {
      const tag = k.pk.S.slice(cfg.tagNamespace.length);
      const rec = opts.tags?.[tag];
      if (!rec) continue;
      const item: Record<string, unknown> = { pk: { S: k.pk.S }, sk: { S: "#META" } };
      if (rec.expired !== undefined) item.expired = { N: String(rec.expired) };
      if (rec.stale !== undefined) item.stale = { N: String(rec.stale) };
      items.push(item);
    }
    return new Response(JSON.stringify({ Responses: { [cfg.table]: items } }), {
      status: 200,
    });
  }) as typeof fetch;
  return { signedFetch, s3Calls, ddbCalls };
}

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

// A Map-backed stand-in for caches.default. `inert` models *.workers.dev, where
// put() is silently discarded and match() never hits, so the snapshot read has
// to degrade to a direct store GET.
function fakeCache(opts: { inert?: boolean } = {}) {
  const stored = new Map<string, string>();
  const calls = { match: 0, put: 0 };
  return {
    calls,
    async match(request: Request): Promise<Response | undefined> {
      calls.match++;
      const body = stored.get(request.url);
      return body === undefined ? undefined : new Response(body);
    },
    async put(request: Request, response: Response): Promise<void> {
      calls.put++;
      const body = await response.text();
      if (!opts.inert) stored.set(request.url, body);
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

const s3Key = (routePath: string) =>
  `${cfg.prefix}/cache/${routePath === "/" ? "index" : routePath.replace(/^\//, "")}.cache.json`;

function appPage(opts: { tags?: string; lastModified?: number; postponed?: unknown } = {}) {
  return {
    lastModified: opts.lastModified ?? 1_000,
    value: {
      kind: "APP_PAGE",
      html: "<html>hi</html>",
      rscData: btoa("RSC-PAYLOAD"),
      status: 200,
      headers: opts.tags ? { "x-next-cache-tags": opts.tags } : {},
      ...(opts.postponed !== undefined ? { postponed: opts.postponed } : {}),
    },
  };
}

// Most cases here are about whether an entry serves at all, which is the
// complete-entry contract; `served` narrows to that so they read as before. The
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

describe("readInterceptionConfig", () => {
  it("is null unless every binding is present", () => {
    expect(readInterceptionConfig({})).toBeNull();
    expect(
      readInterceptionConfig({
        OCEL_EDGE_ACCESS_KEY_ID: "a",
        OCEL_EDGE_SECRET_KEY: "s",
        OCEL_AWS_REGION: "us-east-1",
        OCEL_ISR_BUCKET: "b",
        OCEL_STATE_TABLE: "t",
        OCEL_ISR_PREFIX: "p",
        // missing tag namespace
      }),
    ).toBeNull();
  });

  it("builds a config when all bindings are present", () => {
    const c = readInterceptionConfig({
      OCEL_EDGE_ACCESS_KEY_ID: "a",
      OCEL_EDGE_SECRET_KEY: "s",
      OCEL_AWS_REGION: "us-east-1",
      OCEL_ISR_BUCKET: "b",
      OCEL_STATE_TABLE: "t",
      OCEL_ISR_PREFIX: "p",
      OCEL_ISR_TAG_NAMESPACE: "TAG#p#",
    });
    expect(c).toMatchObject({ accessKeyId: "a", bucket: "b", tagNamespace: "TAG#p#" });
  });
});

describe("intercept", () => {
  it("serves html for a fresh untagged page and never reads DynamoDB", async () => {
    const aws = fakeAws({ entries: { [s3Key("/blog")]: appPage() } });
    const res = await served(req(), target(), cfg, { ...aws, now: () => 2_000 });

    expect(res).not.toBeNull();
    expect(res!.status).toBe(200);
    expect(res!.headers.get("content-type")).toBe("text/html; charset=utf-8");
    expect(await res!.text()).toBe("<html>hi</html>");
    // Entry is 1s old (lastModified 1_000, now 2_000), so the CDN gets the
    // remaining window, not the full 60s.
    expect(res!.headers.get("cache-control")).toBe("s-maxage=59");
    // Marks the serve as an interception hit, not a Lambda-origin fill.
    expect(res!.headers.get("x-ocel-isr")).toBe("HIT");
    expect(aws.ddbCalls.length).toBe(0);
  });

  it("serves the RSC payload when the request negotiates RSC", async () => {
    const aws = fakeAws({ entries: { [s3Key("/blog")]: appPage() } });
    const res = await served(
      req({ headers: { RSC: "1" } }),
      target(),
      cfg,
      { ...aws, now: () => 2_000 },
    );

    expect(res!.headers.get("content-type")).toBe("text/x-component");
    expect(await res!.text()).toBe("RSC-PAYLOAD");
  });

  it("fails open (null) on an S3 miss", async () => {
    const aws = fakeAws({ entries: {} });
    expect(await served(req(), target(), cfg, { ...aws, now: () => 2_000 })).toBeNull();
  });

  it("fails open past the revalidate window (time-based expiry)", async () => {
    const aws = fakeAws({ entries: { [s3Key("/blog")]: appPage({ lastModified: 1_000 }) } });
    // 61s later, revalidate is 60s.
    const res = await served(req(), target({ revalidate: 60 }), cfg, {
      ...aws,
      now: () => 1_000 + 61_000,
    });
    expect(res).toBeNull();
  });

  it("stays fresh within the window with a false (static) revalidate", async () => {
    const aws = fakeAws({ entries: { [s3Key("/blog")]: appPage({ lastModified: 1_000 }) } });
    const res = await served(req(), target({ revalidate: false }), cfg, {
      ...aws,
      now: () => 1_000 + 10 * 365 * 86400_000,
    });
    expect(res).not.toBeNull();
    expect(res!.headers.get("cache-control")).toBe("s-maxage=31536000");
  });

  it("consults DynamoDB and fails open when a tag was revalidated after the entry", async () => {
    const aws = fakeAws({
      entries: { [s3Key("/blog")]: appPage({ tags: "products", lastModified: 1_000 }) },
      tags: { products: { expired: 1_500 } },
    });
    const res = await served(req(), target(), cfg, { ...aws, now: () => 2_000 });
    expect(res).toBeNull();
    expect(aws.ddbCalls.length).toBe(1);
  });

  it("serves a tagged page whose tag expired before the entry was written", async () => {
    const aws = fakeAws({
      entries: { [s3Key("/blog")]: appPage({ tags: "products", lastModified: 1_000 }) },
      tags: { products: { expired: 500 } },
    });
    const res = await served(req(), target(), cfg, { ...aws, now: () => 2_000 });
    expect(res).not.toBeNull();
  });

  it("fails open when DynamoDB errors", async () => {
    const aws = fakeAws({
      entries: { [s3Key("/blog")]: appPage({ tags: "products", lastModified: 1_000 }) },
      tags: {},
      ddbStatus: 500,
    });
    const res = await served(req(), target(), cfg, { ...aws, now: () => 2_000 });
    expect(res).toBeNull();
  });

  it("returns a PPR shell — not a complete response — for a postponed page", async () => {
    const aws = fakeAws({
      entries: { [s3Key("/blog")]: appPage({ postponed: "STATE" }) },
    });
    const outcome = await intercept(req(), target(), cfg, {
      ...aws,
      now: () => 2_000,
    });
    expect(outcome?.kind).toBe("ppr");
  });

  it("serves an APP_ROUTE body with its stored headers verbatim", async () => {
    const entry = {
      lastModified: 1_000,
      value: {
        kind: "APP_ROUTE",
        body: btoa("{\"ok\":true}"),
        status: 201,
        headers: { "content-type": "application/json" },
      },
    };
    const aws = fakeAws({ entries: { [s3Key("/blog")]: entry } });
    const res = await served(req(), target(), cfg, { ...aws, now: () => 2_000 });
    expect(res!.status).toBe(201);
    expect(res!.headers.get("content-type")).toBe("application/json");
    expect(await res!.text()).toBe('{"ok":true}');
  });
});

describe("intercept through the object-store binding", () => {
  const storeDeps = (
    store: ReturnType<typeof fakeStore>,
    over: Partial<InterceptDeps> = {},
  ): InterceptDeps & { s3Calls: string[]; ddbCalls: unknown[] } => ({
    ...fakeAws({}),
    store,
    ...over,
  });

  it("serves a hit from the binding without making a single AWS call", async () => {
    const store = fakeStore({ [s3Key("/blog")]: JSON.stringify(appPage()) });
    const deps = storeDeps(store, { now: () => 2_000 });
    const res = await served(req(), target(), cfg, deps);

    expect(res!.status).toBe(200);
    expect(await res!.text()).toBe("<html>hi</html>");
    expect(res!.headers.get("x-ocel-isr")).toBe("HIT");
    expect(store.gets).toEqual([s3Key("/blog")]);
    expect(deps.s3Calls).toEqual([]);
    expect(deps.ddbCalls).toEqual([]);
  });

  it("decides tag expiry from the snapshot, never DynamoDB", async () => {
    const store = fakeStore({
      [s3Key("/blog")]: JSON.stringify(appPage({ tags: "products", lastModified: 1_000 })),
      [snapshotKey]: JSON.stringify(snapshot({ records: { products: { expired: 1_500 } } })),
    });
    const deps = storeDeps(store, { now: () => 2_000 });

    expect(await served(req(), target(), cfg, deps)).toBeNull();
    expect(store.gets).toContain(snapshotKey);
    expect(deps.ddbCalls).toEqual([]);
  });

  it("serves when the snapshot's expiry predates the entry", async () => {
    const store = fakeStore({
      [s3Key("/blog")]: JSON.stringify(appPage({ tags: "products", lastModified: 1_000 })),
      [snapshotKey]: JSON.stringify(snapshot({ records: { products: { expired: 500 } } })),
    });
    const res = await served(req(), target(), cfg, storeDeps(store, { now: () => 2_000 }));
    expect(res).not.toBeNull();
  });

  it("serves a tagged page the snapshot has no record for", async () => {
    const store = fakeStore({
      [s3Key("/blog")]: JSON.stringify(appPage({ tags: "products", lastModified: 1_000 })),
      [snapshotKey]: JSON.stringify(snapshot()),
    });
    const res = await served(req(), target(), cfg, storeDeps(store, { now: () => 2_000 }));
    expect(res).not.toBeNull();
  });

  it("falls open on a store miss for the entry", async () => {
    const store = fakeStore({});
    expect(
      await served(req(), target(), cfg, storeDeps(store, { now: () => 2_000 })),
    ).toBeNull();
  });

  it("falls open when the store errors", async () => {
    const store = fakeStore({}, { fail: true });
    expect(
      await served(req(), target(), cfg, storeDeps(store, { now: () => 2_000 })),
    ).toBeNull();
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
        [s3Key("/blog")]: JSON.stringify(appPage({ tags: "products", lastModified: 1_000 })),
        ...(body ? { [snapshotKey]: body } : {}),
      });
      const deps = storeDeps(store, { now: () => 2_000 });
      expect(await served(req(), target(), cfg, deps)).toBeNull();
      expect(deps.ddbCalls).toEqual([]);
    });
  }

  it("reads the snapshot once across requests when the PoP cache works", async () => {
    const entry = JSON.stringify(appPage({ tags: "products", lastModified: 1_000 }));
    const store = fakeStore({
      [s3Key("/blog")]: entry,
      [snapshotKey]: JSON.stringify(snapshot()),
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
    const store = fakeStore({
      [s3Key("/blog")]: JSON.stringify(appPage({ tags: "products", lastModified: 1_000 })),
      [snapshotKey]: JSON.stringify(snapshot()),
    });
    const snapshotCache = fakeCache();

    await served(req(), target(), cfg, storeDeps(store, { snapshotCache, now: () => 2_000 }));
    await served(req(), target(), cfg, storeDeps(store, { snapshotCache, now: () => 2_100 }));

    expect(snapshotCache.calls.match).toBe(1);
    expect(store.gets.filter((k) => k === snapshotKey).length).toBe(1);
  });

  it("stays correct with an inert PoP cache, paying a store read per request", async () => {
    const store = fakeStore({
      [s3Key("/blog")]: JSON.stringify(appPage({ tags: "products", lastModified: 1_000 })),
      [snapshotKey]: JSON.stringify(snapshot({ records: { products: { expired: 1_500 } } })),
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
    const store = fakeStore({
      [s3Key("/blog")]: JSON.stringify(appPage({ tags: "products", lastModified: 1_000 })),
      [snapshotKey]: JSON.stringify(snapshot({ validUntil: 10_000 })),
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

  const read = (t: InterceptTarget, entries: Record<string, unknown>, now: number) =>
    intercept(req(), t, cfg, { ...fakeAws({ entries }), now: () => now });

  it("hands back the shell, the postponed state, and no shared-cache claim", async () => {
    const outcome = await read(pprTarget(), { [s3Key("/blog")]: pprEntry() }, 2_000);

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
      { ...fakeAws({ entries: { [s3Key("/blog")]: pprEntry() } }), now: () => 2_000 },
    );

    expect(outcome).toMatchObject({ kind: "ppr", postponed: "STATE" });
    const shell = (outcome as { shell: Response }).shell;
    expect(shell.headers.get("content-type")).toBe("text/x-component");
    expect(await shell.text()).toBe("RSC-PAYLOAD");
  });

  it("still serves past initialRevalidate, marked stale", async () => {
    const outcome = await read(
      pprTarget(),
      { [s3Key("/blog")]: pprEntry({ lastModified: 1_000 }) },
      1_000 + 61_000,
    );

    expect(outcome).toMatchObject({ kind: "ppr", stale: true });
  });

  it("falls open past initialExpiration", async () => {
    const outcome = await read(
      pprTarget({ expiration: 3600 }),
      { [s3Key("/blog")]: pprEntry({ lastModified: 1_000 }) },
      1_000 + 3_600_000,
    );

    expect(outcome).toBeNull();
  });

  it("still refuses a PPR entry whose tags were invalidated", async () => {
    const aws = fakeAws({
      entries: { [s3Key("/blog")]: pprEntry({ tags: "posts" }) },
      tags: { posts: { expired: 1_500 } },
    });
    expect(
      await intercept(req(), pprTarget(), cfg, { ...aws, now: () => 2_000 }),
    ).toBeNull();
  });

  it("resumes a concrete path from the route's param-agnostic fallback shell", async () => {
    const outcome = await read(
      pprTarget({ routePath: "/posts/7", fallbackPath: "/posts/[id]" }),
      { [s3Key("/posts/[id]")]: pprEntry() },
      2_000,
    );

    expect(outcome).toMatchObject({ kind: "ppr", postponed: "STATE" });
  });

  it("never serves a complete entry found under the dynamic pattern", async () => {
    // Without a postponed state that entry is some other path's rendered page.
    const outcome = await read(
      pprTarget({ routePath: "/posts/7", fallbackPath: "/posts/[id]" }),
      { [s3Key("/posts/[id]")]: appPage() },
      2_000,
    );

    expect(outcome).toBeNull();
  });

  it("prefers the concrete entry over the fallback shell", async () => {
    const outcome = await read(
      pprTarget({ routePath: "/posts/7", fallbackPath: "/posts/[id]" }),
      {
        [s3Key("/posts/7")]: appPage(),
        [s3Key("/posts/[id]")]: pprEntry(),
      },
      2_000,
    );

    expect(outcome?.kind).toBe("complete");
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
      [s3Key("/blog")]: JSON.stringify(
        appPage({ tags: "products", lastModified: genesis.deployedAt }),
      ),
      [snapshotKey]: genesisSnapshot,
    });
    const deps = { ...fakeAws({}), store, now: () => within };

    const res = await served(req(), target({ revalidate: false }), cfg, deps);
    expect(res).not.toBeNull();
    expect(deps.ddbCalls).toEqual([]);
  });

  it("stops being trusted at the validity window the publisher declared", async () => {
    const store = fakeStore({
      [s3Key("/blog")]: JSON.stringify(
        appPage({ tags: "products", lastModified: genesis.deployedAt }),
      ),
      [snapshotKey]: genesisSnapshot,
    });
    const res = await served(req(), target({ revalidate: false }), cfg, {
      ...fakeAws({}),
      store,
      now: () => genesis.validUntil,
    });
    expect(res).toBeNull();
  });

  it("is addressed at the key the publisher writes it to", () => {
    expect(snapshotKey).toBe("prod/proj/app/build/tag-clock.json");
  });
});
