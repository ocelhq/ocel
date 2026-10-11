import { createExecutionContext, env } from "cloudflare:test";
import { beforeEach, describe, expect, it } from "vitest";
import type { Env } from "../src/env";
import type { PurgeQueue } from "../src/purge-queue";
import { openPurgeQueue } from "../src/purge-request";
import type { TagTimes } from "../src/purge-times";
import { Serve } from "../src/serve";
import {
  buildOriginCall,
  ORIGIN_PURGE_SCOPE,
  RENDER_RELEASE_HEADER,
  SERVE_UNSHARED_HEADER,
} from "../src/serve-key";
import { FN_URL, withGlobalFetch } from "./origin-deps";

declare module "cloudflare:test" {
  interface ProvidedEnv {
    PURGE_QUEUE: DurableObjectNamespace<PurgeQueue>;
  }
}

const isrPrefix = "prod/p1/web/r1a2b3c4d/isr";
const coordinates = { host: "shop.example.com", app: "web", release: "r1a2b3c4d" };
const entryKey = `${isrPrefix}/cache/blog/a.cache.json`;

const record = {
  app: "web",
  framework: "next",
  release: "build-1~fp1",
  buildId: "build-1",
  functionUrls: { "/blog": `${FN_URL}stage` },
  assetPrefix: "prod/p1/web/r1a2b3c4d/assets",
  isrPrefix,
  createdAt: 1,
};

const storedEntry = () => ({
  lastModified: Date.now() - 10_000,
  value: {
    kind: "APP_PAGE",
    status: 200,
    html: "<html>from the store</html>",
    headers: {
      "content-type": "text/html; charset=utf-8",
      "x-next-cache-tags": "_N_T_/blog/a,posts",
    },
  },
});

interface Harness {
  serve: Serve;
  settled: () => Promise<void>;
  originCalls: Request[];
  timeReads: string[][];
  fetchOrigin: <T>(run: () => Promise<T>) => Promise<T>;
}

function harness(
  options: {
    times?: TagTimes | Error;
    originStatus?: number;
    entry?: object | null;
    release?: Record<string, unknown>;
    call?: Record<string, unknown>;
    queues?: unknown;
  } = {},
): Harness {
  const pending: Promise<unknown>[] = [];
  const timeReads: string[][] = [];
  const originCalls: Request[] = [];
  const { times = {}, originStatus = 200 } = options;
  const ctx = Object.assign(createExecutionContext(), {
    props: ORIGIN_PURGE_SCOPE,
    waitUntil: (promise: Promise<unknown>) => void pending.push(promise),
    exports: {
      PurgeQueue: options.queues ?? {
        getByName: () => ({
          raise: async () => {},
          record: async () => {},
          readTimes: async (tags: string[]) => {
            timeReads.push(tags);
            if (times instanceof Error) throw times;
            return times;
          },
        }),
      },
    },
  });
  const released = {
    slug: "p1",
    isrPrefix,
    ...options.release,
  };
  const serve = new Serve(
    ctx as never,
    {
      RELEASES: {
        readServedRecord: async () => ({ kind: "record", record }),
      },
      OCEL_CACHE_STORE: env.TAG_SNAPSHOT_STORE,
      OCEL_EDGE_ACCESS_KEY_ID: "AKIAEXAMPLE",
      OCEL_EDGE_SECRET_KEY: "secretkey",
    } as unknown as Env,
  );
  const headerValue = JSON.stringify({
    ...released,
    entry: { path: "/blog/a" },
    ...options.call,
  });
  const request = (path = "blog/a") =>
    new Request(buildOriginCall(`${FN_URL}${path}`, coordinates).url, {
      headers: { [RENDER_RELEASE_HEADER]: headerValue },
    });
  const harnessed = {
    serve,
    originCalls,
    timeReads,
    settled: async () => void (await Promise.all(pending)),
    request,
    fetchOrigin: async <T>(run: () => Promise<T>) =>
      withGlobalFetch(
        (async (input: RequestInfo | URL, init?: RequestInit) => {
          originCalls.push(new Request(input as RequestInfo, init));
          return new Response("<html>from the origin</html>", {
            status: originStatus,
            headers: { "cache-control": "s-maxage=300" },
          });
        }) as typeof fetch,
        run,
      ),
  };
  return harnessed as unknown as Harness & { request: typeof request };
}

const fallbackKey = `${isrPrefix}/cache/blog/[id].cache.json`;

async function seed(entry: object | null, key = entryKey): Promise<void> {
  if (entry) await env.TAG_SNAPSHOT_STORE.put(key, JSON.stringify(entry));
  else await env.TAG_SNAPSHOT_STORE.delete(key);
}

type Used = Harness & { request: (path?: string) => Request };

beforeEach(async () => {
  await seed(null);
  await seed(null, fallbackKey);
});

describe("Serve answering a miss from the durable store", () => {
  it("answers the stored page without calling the origin, for Workers Cache to revalidate on the next read", async () => {
    await seed(storedEntry());
    const h = harness() as Used;

    const response = await h.fetchOrigin(async () => {
      const answered = await h.serve.fetch(h.request());
      await h.settled();
      return answered;
    });

    expect(await response.text()).toBe("<html>from the store</html>");
    expect(response.headers.get("cache-tag")).toContain("r1a2b3c4d|posts");
    expect(response.headers.get("cloudflare-cdn-cache-control")).toMatch(
      /^max-age=0, stale-while-revalidate=\d+/,
    );
    expect(response.headers.get("etag")).not.toBeNull();
    expect(h.originCalls).toEqual([]);
  });

  it("answers Workers Cache's revalidation of a stored page from the origin, which then keeps the origin's own lifetime", async () => {
    await seed(storedEntry());
    const h = harness() as Used;
    const stored = await h.fetchOrigin(() => h.serve.fetch(h.request()));
    const revalidation = h.request();
    revalidation.headers.set("if-none-match", stored.headers.get("etag")!);

    const response = await h.fetchOrigin(() => h.serve.fetch(revalidation));

    expect(await response.text()).toBe("<html>from the origin</html>");
    expect(response.headers.get("cloudflare-cdn-cache-control")).toMatch(/^max-age=300\b/);
    expect(h.originCalls.map(({ url }) => url)).toEqual([`${FN_URL}blog/a`]);
  });

  it("goes to the origin, and only there, once a tag of the entry was purged after it was written", async () => {
    await seed(storedEntry());
    const h = harness({ times: { "r1a2b3c4d|posts": { purgedAt: Date.now() - 1_000 } } }) as Used;

    const response = await h.fetchOrigin(async () => {
      const answered = await h.serve.fetch(h.request());
      await h.settled();
      return answered;
    });

    expect(await response.text()).toBe("<html>from the origin</html>");
    expect(response.headers.has("x-ocel-entry-source")).toBe(false);
    expect(h.originCalls).toHaveLength(1);
  });

  it("serves a tag whose purge it never heard of, and Workers Cache's next revalidation replaces it with the origin's render", async () => {
    await seed(storedEntry());
    const h = harness({ times: {} }) as Used;

    const served = await h.fetchOrigin(() => h.serve.fetch(h.request()));
    const revalidation = h.request();
    revalidation.headers.set("if-none-match", served.headers.get("etag")!);
    const corrected = await h.fetchOrigin(() => h.serve.fetch(revalidation));

    expect(await served.text()).toBe("<html>from the store</html>");
    expect(await corrected.text()).toBe("<html>from the origin</html>");
    expect(h.originCalls).toHaveLength(1);
  });

  it("goes to the origin when the purge queue cannot be reached", async () => {
    await seed(storedEntry());
    const h = harness({ times: new Error("queue unreachable") }) as Used;

    const response = await h.fetchOrigin(() => h.serve.fetch(h.request()));

    expect(await response.text()).toBe("<html>from the origin</html>");
  });

  it("goes to the origin when the store has no entry", async () => {
    const h = harness() as Used;

    const response = await h.fetchOrigin(() => h.serve.fetch(h.request()));

    expect(await response.text()).toBe("<html>from the origin</html>");
    expect(h.originCalls).toHaveLength(1);
  });

  it("goes to the origin for a request that carries a query", async () => {
    await seed(storedEntry());
    const h = harness() as Used;

    const response = await h.fetchOrigin(() => h.serve.fetch(h.request("blog/a?page=2")));

    expect(await response.text()).toBe("<html>from the origin</html>");
  });

  it("reads no store for a call that names no entry", async () => {
    await seed(storedEntry());
    const h = harness() as Used;
    const request = new Request(buildOriginCall(`${FN_URL}blog/a`, coordinates).url, {
      headers: { [RENDER_RELEASE_HEADER]: JSON.stringify({ slug: "p1", isrPrefix }) },
    });

    const response = await h.fetchOrigin(() => h.serve.fetch(request));

    expect(await response.text()).toBe("<html>from the origin</html>");
  });
});

describe("Serve reading tag times from the purge queue itself", () => {
  it("skips the store on the next miss once the purge queue recorded a purge of a tag of the entry", async () => {
    await seed(storedEntry());
    const queues = { PurgeQueue: env.PURGE_QUEUE };
    const h = harness({ queues: queues.PurgeQueue }) as Used;

    const before = await h.fetchOrigin(() => h.serve.fetch(h.request()));
    await openPurgeQueue(queues).record("purge", [
      { tag: "r1a2b3c4d|posts", raisedAt: Date.now() },
    ]);
    const after = await h.fetchOrigin(() => h.serve.fetch(h.request()));

    expect(await before.text()).toBe("<html>from the store</html>");
    expect(await after.text()).toBe("<html>from the origin</html>");
  });
});

describe("Serve answering the fallback shell every param of a route shares", () => {
  const fallbackEntry = () => ({
    lastModified: Date.now() - 10_000,
    value: {
      kind: "APP_PAGE",
      status: 200,
      html: "<html>fallback shell</html>",
      postponed: "STATE",
      headers: {
        "content-type": "text/html; charset=utf-8",
        "x-next-cache-tags": "_N_T_/blog/[id],pprid",
      },
    },
  });
  const shared = (options: { times?: TagTimes | Error } = {}) =>
    harness({
      ...options,
      call: {
        sharesFallbackShell: true,
        entry: { path: "/blog/[id]", fallbackPath: "/blog/[id]" },
      },
    }) as Used;

  it("fills it from the deploy-time fallback entry and never asks the origin", async () => {
    await seed(fallbackEntry(), fallbackKey);
    const h = shared();

    const response = await h.fetchOrigin(() => h.serve.fetch(h.request("blog/[id]")));

    expect(await response.text()).toBe("<html>fallback shell</html>");
    expect(response.headers.get("cloudflare-cdn-cache-control")).toMatch(
      /^max-age=\d+, stale-if-error/,
    );
    expect(response.headers.has(SERVE_UNSHARED_HEADER)).toBe(false);
    expect(h.originCalls).toEqual([]);
  });

  it("answers Workers Cache's revalidation of it from the store too, never from the origin", async () => {
    await seed(fallbackEntry(), fallbackKey);
    const h = shared();
    const revalidation = h.request("blog/[id]");
    revalidation.headers.set("if-none-match", 'W/"anything"');

    const response = await h.fetchOrigin(() => h.serve.fetch(revalidation));

    expect(await response.text()).toBe("<html>fallback shell</html>");
    expect(h.originCalls).toEqual([]);
  });

  it("answers that it is unavailable, and keeps that answer, when there is no fallback entry", async () => {
    const h = shared();

    const response = await h.fetchOrigin(() => h.serve.fetch(h.request("blog/[id]")));

    expect(response.headers.get(SERVE_UNSHARED_HEADER)).toBe("1");
    expect(response.headers.has("cloudflare-cdn-cache-control")).toBe(true);
    expect(h.originCalls).toEqual([]);
  });

  it("answers that it is unavailable, and keeps that answer, once a tag of the fallback was invalidated", async () => {
    await seed(fallbackEntry(), fallbackKey);
    const h = shared({ times: { "r1a2b3c4d|pprid": { invalidatedAt: Date.now() } } });

    const response = await h.fetchOrigin(() => h.serve.fetch(h.request("blog/[id]")));

    expect(response.headers.get(SERVE_UNSHARED_HEADER)).toBe("1");
    expect(response.headers.has("cloudflare-cdn-cache-control")).toBe(true);
    expect(h.originCalls).toEqual([]);
  });

  it("answers that it is unavailable, without keeping that answer, when the purge queue cannot be reached", async () => {
    await seed(fallbackEntry(), fallbackKey);
    const h = shared({ times: new Error("queue unreachable") });

    const response = await h.fetchOrigin(() => h.serve.fetch(h.request("blog/[id]")));

    expect(response.headers.get(SERVE_UNSHARED_HEADER)).toBe("1");
    expect(response.headers.get("cache-control")).toBe("private, no-store");
    expect(h.originCalls).toEqual([]);
  });
});
