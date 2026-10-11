import { createExecutionContext, env } from "cloudflare:test";
import { beforeEach, describe, expect, it } from "vitest";
import type { Env } from "../src/env";
import type { TagTimes } from "../src/purge-times";
import { dropRefreshes, Serve } from "../src/serve";
import { buildOriginCall, ORIGIN_PURGE_SCOPE, RENDER_RELEASE_HEADER } from "../src/serve-key";
import { dropTimesMemo } from "../src/store-read";
import { FN_URL, withGlobalFetch } from "./origin-deps";

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
      PurgeQueue: {
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
  const headerValue = JSON.stringify({ ...released, entry: { path: "/blog/a" } });
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

async function seed(entry: object | null): Promise<void> {
  if (entry) await env.TAG_SNAPSHOT_STORE.put(entryKey, JSON.stringify(entry));
  else await env.TAG_SNAPSHOT_STORE.delete(entryKey);
}

type Used = Harness & { request: (path?: string) => Request };

beforeEach(async () => {
  dropTimesMemo();
  dropRefreshes();
  await seed(null);
});

describe("Serve answering a miss from the durable store", () => {
  it("answers the stored page and refreshes from the origin behind the response, once", async () => {
    await seed(storedEntry());
    const h = harness() as Used;

    const response = await h.fetchOrigin(async () => {
      const answered = await h.serve.fetch(h.request());
      await h.settled();
      return answered;
    });

    expect(await response.text()).toBe("<html>from the store</html>");
    expect(response.headers.get("x-ocel-entry-source")).toBe("store");
    expect(response.headers.get("cache-tag")).toContain("r1a2b3c4d|posts");
    expect(h.originCalls.map(({ url }) => url)).toEqual([`${FN_URL}blog/a`]);
  });

  it("refreshes once however many visitors miss in the same data centre at the same time", async () => {
    await seed(storedEntry());
    const h = harness() as Used;

    await h.fetchOrigin(async () => {
      await Promise.all([h.serve.fetch(h.request()), h.serve.fetch(h.request())]);
      await h.settled();
    });

    expect(h.originCalls).toHaveLength(1);
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

  it("serves a tag whose purge it never heard of, then lets the refresh correct it", async () => {
    await seed(storedEntry());
    const h = harness({ times: {} }) as Used;

    const response = await h.fetchOrigin(async () => {
      const answered = await h.serve.fetch(h.request());
      await h.settled();
      return answered;
    });

    expect(response.headers.get("x-ocel-entry-source")).toBe("store");
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

  it("answers the stored page even when the refresh fails", async () => {
    await seed(storedEntry());
    const h = harness({ originStatus: 500 }) as Used;

    const response = await h.fetchOrigin(async () => {
      const answered = await h.serve.fetch(h.request());
      await h.settled();
      return answered;
    });

    expect(await response.text()).toBe("<html>from the store</html>");
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
