import { createExecutionContext, env } from "cloudflare:test";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Env } from "../src/env";
import type { PurgeQueue } from "../src/purge-queue";
import { openPurgeQueue } from "../src/purge-request";
import type { TagTimes } from "../src/purge-times";
import { REGENERATION_HOLD_MS, Serve } from "../src/serve";
import {
  buildOriginCall,
  ORIGIN_PURGE_SCOPE,
  RENDER_RELEASE_HEADER,
  SERVE_NO_SHELL_HEADER,
  SERVE_UNSHARED_HEADER,
} from "../src/serve-key";
import { FN_URL, withGlobalFetch } from "./origin-deps";

declare module "cloudflare:test" {
  interface ProvidedEnv {
    PURGE_QUEUE: DurableObjectNamespace<PurgeQueue>;
  }
}

const BUCKET_HOST = "assets-bucket.s3.us-east-1.amazonaws.com";

const objects = new Map<string, string>();

function viaBucket(next: typeof fetch): typeof fetch {
  return (async (input: RequestInfo | URL, init?: RequestInit) => {
    const { host, pathname } = new URL(new Request(input as RequestInfo, init).url);
    if (host !== BUCKET_HOST) return next(input, init);
    const body = objects.get(decodeURIComponent(pathname.slice(1)));
    return body === undefined ? new Response("<Error/>", { status: 404 }) : new Response(body);
  }) as typeof fetch;
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
    origin?: () => Promise<Response>;
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
      OCEL_ASSET_BUCKET: "assets-bucket",
      OCEL_AWS_REGION: "us-east-1",
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
        viaBucket((async (input: RequestInfo | URL, init?: RequestInit) => {
          originCalls.push(new Request(input as RequestInfo, init));
          if (options.origin) return options.origin();
          return new Response("<html>from the origin</html>", {
            status: originStatus,
            headers: { "cache-control": "s-maxage=300" },
          });
        }) as typeof fetch),
        run,
      ),
  };
  return harnessed as unknown as Harness & { request: typeof request };
}

const fallbackKey = `${isrPrefix}/cache/blog/[id].cache.json`;

async function seed(entry: object | null, key = entryKey): Promise<void> {
  if (entry) objects.set(key, JSON.stringify(entry));
  else objects.delete(key);
}

type Used = Harness & { request: (path?: string) => Request };

const realFetch = globalThis.fetch;

beforeEach(async () => {
  globalThis.fetch = viaBucket(realFetch);
  await seed(null);
  await seed(null, fallbackKey);
});

afterEach(() => {
  globalThis.fetch = realFetch;
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

  it("answers the stored page for a query its route's path determines", async () => {
    await seed(storedEntry());
    const h = harness({ call: { entry: { path: "/blog/a", pathDeterminesQuery: true } } }) as Used;

    const response = await h.fetchOrigin(() => h.serve.fetch(h.request("blog/a?nxtPid=a")));

    expect(await response.text()).toBe("<html>from the store</html>");
    expect(h.originCalls).toEqual([]);
  });

  it("answers a segment prefetch whose query its route's path determines from the stored segment", async () => {
    await seed({
      ...storedEntry(),
      value: {
        ...storedEntry().value,
        segmentHeaders: { "content-type": "text/x-component" },
        segmentData: { "/_tree": Buffer.from("tree segment").toString("base64") },
      },
    });
    const h = harness({ call: { entry: { path: "/blog/a", pathDeterminesQuery: true } } }) as Used;
    const prefetch = h.request("blog/a?nxtPid=a");
    prefetch.headers.set("rsc", "1");
    prefetch.headers.set("next-router-prefetch", "1");
    prefetch.headers.set("next-router-segment-prefetch", "/_tree");

    const response = await h.fetchOrigin(() => h.serve.fetch(prefetch));

    expect(await response.text()).toBe("tree segment");
    expect(h.originCalls).toEqual([]);
  });

  it("goes to the origin for a query named like a route parameter on a route whose every query varies the page", async () => {
    await seed(storedEntry());
    const h = harness() as Used;

    const response = await h.fetchOrigin(() => h.serve.fetch(h.request("blog/a?nxtPx=1")));

    expect(await response.text()).toBe("<html>from the origin</html>");
    expect(h.originCalls).toHaveLength(1);
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

describe("Serve answering the shell of a PPR document", () => {
  const shellEntry = () => ({
    lastModified: Date.now() - 10_000,
    value: {
      kind: "APP_PAGE",
      status: 200,
      html: "<html>shell</html>",
      postponed: "STATE",
      headers: {
        "content-type": "text/html; charset=utf-8",
        "x-next-cache-tags": "_N_T_/blog/a,posts",
      },
    },
  });
  const shellRequest = (h: Used, headers: Record<string, string> = {}) => {
    const plain = h.request();
    const url = buildOriginCall(`${FN_URL}blog/a`, { ...coordinates, variant: ".ppr-shell" }).url;
    return new Request(url, {
      headers: { [RENDER_RELEASE_HEADER]: plain.headers.get(RENDER_RELEASE_HEADER)!, ...headers },
    });
  };

  it("frames the stored shell with its postponed state and never asks the origin", async () => {
    await seed(shellEntry());
    const h = harness() as Used;

    const response = await h.fetchOrigin(() => h.serve.fetch(shellRequest(h)));

    expect(await response.text()).toBe("STATE<html>shell</html>");
    expect(response.headers.get("x-ocel-postponed-length")).toBe("5");
    expect(h.originCalls).toEqual([]);
  });

  it.each([
    [
      "frames the stored shell for a query its route's path determines",
      { pathDeterminesQuery: true },
      "STATE<html>shell</html>",
    ],
    ["answers that it has no shell for a query that varies the page", {}, null],
  ])("%s", async (_, entry, body) => {
    await seed(shellEntry());
    const h = harness({ call: { entry: { path: "/blog/a", ...entry } } }) as Used;
    const url = buildOriginCall(`${FN_URL}blog/a?nxtPid=a`, {
      ...coordinates,
      variant: ".ppr-shell",
    }).url;
    const request = new Request(url, {
      headers: { [RENDER_RELEASE_HEADER]: h.request().headers.get(RENDER_RELEASE_HEADER)! },
    });

    const response = await h.fetchOrigin(() => h.serve.fetch(request));

    if (body === null) expect(response.headers.get(SERVE_NO_SHELL_HEADER)).toBe("1");
    else expect(await response.text()).toBe(body);
    expect(h.originCalls).toEqual([]);
  });

  it.each([
    ["the store has no entry", { entry: false, times: {} }],
    [
      "a tag of the entry was purged after it was written",
      { entry: true, times: { "r1a2b3c4d|posts": { purgedAt: Date.now() - 1_000 } } },
    ],
    ["the purge queue cannot be reached", { entry: true, times: new Error("queue unreachable") }],
  ])(
    "answers that it has no shell, keeps nothing and never asks the origin when %s",
    async (_, c) => {
      if (c.entry) await seed(shellEntry());
      const h = harness({ times: c.times }) as Used;

      const response = await h.fetchOrigin(() => h.serve.fetch(shellRequest(h)));

      expect(response.headers.get(SERVE_NO_SHELL_HEADER)).toBe("1");
      expect(response.headers.get("cache-control")).toBe("private, no-store");
      expect(response.headers.has("cloudflare-cdn-cache-control")).toBe(false);
      expect(h.originCalls).toEqual([]);
    },
  );

  it("answers Workers Cache's revalidation of a fresh shell from the store, not the origin", async () => {
    await seed(shellEntry());
    const h = harness() as Used;

    const response = await h.fetchOrigin(() =>
      h.serve.fetch(shellRequest(h, { "if-none-match": 'W/"anything"' })),
    );

    expect(await response.text()).toBe("STATE<html>shell</html>");
    expect(h.originCalls).toEqual([]);
  });

  it("answers the revalidation of an invalidated shell from the store and has the origin regenerate the page", async () => {
    await seed(shellEntry());
    const h = harness({ times: { "r1a2b3c4d|posts": { invalidatedAt: Date.now() } } }) as Used;

    const response = await h.fetchOrigin(async () => {
      const answered = await h.serve.fetch(shellRequest(h, { "if-none-match": 'W/"anything"' }));
      await h.settled();
      return answered;
    });

    expect(await response.text()).toBe("STATE<html>shell</html>");
    expect(h.originCalls.map(({ url }) => url)).toEqual([`${FN_URL}blog/a`]);
    expect(h.originCalls[0].headers.has("if-none-match")).toBe(false);
  });

  const invalidated = () => ({ "r1a2b3c4d|posts": { invalidatedAt: Date.now() } });
  const revalidating = { "if-none-match": 'W/"anything"' };

  function heldOrigin() {
    let finish = () => {};
    let reached = () => {};
    const called = new Promise<void>((resolve) => {
      reached = resolve;
    });
    const origin = async () =>
      new Response(
        new ReadableStream({
          start(controller) {
            controller.enqueue(new TextEncoder().encode("<html>regenerated"));
            finish = () => controller.close();
            reached();
          },
        }),
      );
    return { origin, called, finish: () => finish() };
  }

  function track(pending: Promise<Response>) {
    const state = { answered: false };
    void pending.then(() => {
      state.answered = true;
    });
    return state;
  }

  afterEach(() => {
    vi.useRealTimers();
  });

  it("holds the revalidation of an invalidated shell until the origin has rendered the page in full", async () => {
    await seed(shellEntry());
    const held = heldOrigin();
    const h = harness({ times: invalidated(), origin: held.origin }) as Used;

    await h.fetchOrigin(async () => {
      const pending = h.serve.fetch(shellRequest(h, revalidating));
      const state = track(pending);
      await held.called;
      await new Promise((resolve) => setTimeout(resolve, 50));
      expect(state.answered).toBe(false);

      held.finish();
      expect(await (await pending).text()).toBe("STATE<html>shell</html>");
    });
    expect(h.originCalls).toHaveLength(1);
  });

  it("answers the revalidation of an invalidated shell from the store once the origin has rendered for longer than the hold", async () => {
    await seed(shellEntry());
    const held = heldOrigin();
    const h = harness({ times: invalidated(), origin: held.origin }) as Used;

    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
    await h.fetchOrigin(async () => {
      const pending = h.serve.fetch(shellRequest(h, revalidating));
      const state = track(pending);
      await held.called;
      await vi.advanceTimersByTimeAsync(REGENERATION_HOLD_MS - 1);
      expect(state.answered).toBe(false);

      await vi.advanceTimersByTimeAsync(1);
      expect(await (await pending).text()).toBe("STATE<html>shell</html>");
      held.finish();
      await h.settled();
    });
  });
});

describe("Serve answering the shell of a PPR page the store has only the route's fallback for", () => {
  const fallbackShell = () => ({
    lastModified: Date.now() - 10_000,
    value: {
      kind: "APP_PAGE",
      status: 200,
      html: "<html>fallback shell</html>",
      postponed: "FALLBACK",
      headers: {
        "content-type": "text/html; charset=utf-8",
        "x-next-cache-tags": "_N_T_/blog/[id],posts",
      },
    },
  });
  const call = { call: { entry: { path: "/blog/a", fallbackPath: "/blog/[id]" } } };
  const shellRequest = (h: Used, headers: Record<string, string> = {}) => {
    const plain = h.request();
    const url = buildOriginCall(`${FN_URL}blog/a`, { ...coordinates, variant: ".ppr-shell" }).url;
    return new Request(url, {
      headers: { [RENDER_RELEASE_HEADER]: plain.headers.get(RENDER_RELEASE_HEADER)!, ...headers },
    });
  };

  it("answers the fallback shell without asking the origin", async () => {
    await seed(fallbackShell(), fallbackKey);
    const h = harness(call) as Used;

    const response = await h.fetchOrigin(async () => {
      const answered = await h.serve.fetch(shellRequest(h));
      await h.settled();
      return answered;
    });

    expect(await response.text()).toBe("FALLBACK<html>fallback shell</html>");
    expect(h.originCalls).toEqual([]);
  });

  it("has the origin render the concrete page on the revalidation, so the store holds its own entry", async () => {
    await seed(fallbackShell(), fallbackKey);
    const h = harness(call) as Used;

    const response = await h.fetchOrigin(async () => {
      const answered = await h.serve.fetch(shellRequest(h, { "if-none-match": 'W/"anything"' }));
      await h.settled();
      return answered;
    });

    expect(await response.text()).toBe("FALLBACK<html>fallback shell</html>");
    expect(h.originCalls.map(({ url }) => url)).toEqual([`${FN_URL}blog/a`]);
    expect(h.originCalls[0].headers.has("if-none-match")).toBe(false);
  });
});
