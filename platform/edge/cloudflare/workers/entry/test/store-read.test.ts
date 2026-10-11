import { describe, expect, it } from "vitest";
import type { TagTimes } from "../src/purge-times";
import { readFromStore, type StoreCall, type StoreReadDeps } from "../src/store-read";

const PREFIX = "prod/p1/web/r1a2b3c4d/isr";
const NOW = 1_800_000_000_000;

function entry(over: Record<string, unknown> = {}, lastModified = NOW - 10_000) {
  return {
    lastModified,
    value: {
      kind: "APP_PAGE",
      status: 200,
      html: "<html>page</html>",
      rscData: Buffer.from("rsc-payload").toString("base64"),
      headers: {
        "content-type": "text/html; charset=utf-8",
        "x-next-cache-tags": "_N_T_/layout,_N_T_/blog/a,posts",
      },
      ...over,
    },
  };
}

function harness(
  entries: Record<string, unknown>,
  times: TagTimes | Error = {},
  now = NOW,
): { deps: StoreReadDeps; reads: string[]; timeReads: string[][] } {
  const reads: string[] = [];
  const timeReads: string[][] = [];
  return {
    reads,
    timeReads,
    deps: {
      now: () => now,
      source: {
        async get(key) {
          reads.push(key);
          const found = entries[key];
          return found ? { text: async () => JSON.stringify(found) } : null;
        },
      },
      async readTimes(tags) {
        timeReads.push(tags);
        if (times instanceof Error) throw times;
        return times;
      },
    },
  };
}

const key = (path: string) => `${PREFIX}/cache/${path}.cache.json`;

function call(over: Partial<StoreCall> = {}): StoreCall {
  return {
    isrPrefix: PREFIX,
    release: "r1a2b3c4d",
    entryPath: "/blog/a",
    variant: "",
    headers: new Headers(),
    ...over,
  };
}

describe("reading a page from the durable store on a Serve miss", () => {
  it("answers the page's HTML with the tags the origin checks it by, stamped with the release", async () => {
    const { deps } = harness({ [key("blog/a")]: entry() });

    const answer = await readFromStore(call(), deps);

    expect(answer?.stale).toBe(false);
    expect(await answer?.response.text()).toBe("<html>page</html>");
    expect(answer?.response.headers.get("x-ocel-cache-tag")).toBe(
      "r1a2b3c4d|_N_T_/layout,r1a2b3c4d|_N_T_/blog/a,r1a2b3c4d|posts",
    );
    expect(answer?.response.headers.get("x-ocel-entry-source")).toBe("store");
    expect(answer?.response.headers.get("cache-control")).toBe(
      "s-maxage=0, stale-while-revalidate=31536000",
    );
    expect(answer?.response.headers.get("etag")).toBe(`W/"ocel-store-${NOW - 10_000}"`);
    expect(answer?.response.headers.has("x-next-cache-tags")).toBe(false);
  });

  it("answers an RSC request with the entry's flight payload", async () => {
    const { deps } = harness({ [key("blog/a")]: entry() });

    const answer = await readFromStore(call({ headers: new Headers({ rsc: "1" }) }), deps);

    expect(await answer?.response.text()).toBe("rsc-payload");
    expect(answer?.response.headers.get("content-type")).toBe("text/x-component");
  });

  it("answers a segment prefetch from the entry's segment, as a segment payload", async () => {
    const stored = entry({
      segmentHeaders: { "content-type": "text/x-component" },
      segmentData: { "/_tree": Buffer.from("segment").toString("base64") },
    });
    const { deps } = harness({ [key("blog/a")]: stored });

    const answer = await readFromStore(
      call({ headers: new Headers({ rsc: "1", "next-router-segment-prefetch": "/_tree" }) }),
      deps,
    );

    expect(await answer?.response.text()).toBe("segment");
    expect(answer?.response.headers.get("x-nextjs-postponed")).toBe("2");
  });

  it("frames a PPR shell as its postponed state followed by the shell, with the length of the state", async () => {
    const stored = entry({ postponed: "STATE-é" });
    const { deps } = harness({ [key("blog/a")]: stored });

    const answer = await readFromStore(call({ variant: ".ppr-shell" }), deps);

    const state = new TextEncoder().encode("STATE-é");
    expect(answer?.response.headers.get("x-ocel-postponed-length")).toBe(String(state.byteLength));
    expect(await answer?.response.text()).toBe("STATE-é<html>page</html>");
  });

  it("answers a shell that postponed nothing as the complete page it is", async () => {
    const { deps } = harness({ [key("blog/a")]: entry() });

    const answer = await readFromStore(call({ variant: ".ppr-shell" }), deps);

    expect(answer?.response.headers.has("x-ocel-postponed-length")).toBe(false);
    expect(await answer?.response.text()).toBe("<html>page</html>");
  });

  it("answers a path the build did not prerender with the route's fallback shell", async () => {
    const fallback = entry({ postponed: "STATE" });
    const { deps, reads } = harness({ [key("blog/[id]")]: fallback });

    const answer = await readFromStore(
      call({ entryPath: "/blog/9", fallbackPath: "/blog/[id]", variant: ".ppr-shell" }),
      deps,
    );

    expect(reads).toEqual([key("blog/9"), key("blog/[id]")]);
    expect(await answer?.response.text()).toBe("STATE<html>page</html>");
  });

  it("answers a route handler's body", async () => {
    const stored = {
      lastModified: NOW - 1_000,
      value: {
        kind: "APP_ROUTE",
        status: 200,
        headers: { "content-type": "application/json" },
        body: Buffer.from('{"ok":true}').toString("base64"),
      },
    };
    const { deps } = harness({ [key("api/ping")]: stored });

    const answer = await readFromStore(call({ entryPath: "/api/ping" }), deps);

    expect(await answer?.response.text()).toBe('{"ok":true}');
  });

  it("reads nothing but the page entry objects, and never answers a data-cache entry", async () => {
    const fetchEntry = { lastModified: NOW, value: { kind: "FETCH", data: {}, tags: ["posts"] } };
    const { deps, reads } = harness({ [key("blog/a")]: fetchEntry });

    const answer = await readFromStore(call(), deps);

    expect(answer).toBeNull();
    expect(reads.every((read) => read.startsWith(`${PREFIX}/cache/`))).toBe(true);
  });

  it("goes to the origin when there is no entry", async () => {
    const { deps } = harness({});

    expect(await readFromStore(call(), deps)).toBeNull();
  });
});

describe("whether an entry in the store may still be served", () => {
  it("goes to the origin when a tag of the entry was purged after the entry was written", async () => {
    const { deps } = harness(
      { [key("blog/a")]: entry() },
      { "r1a2b3c4d|posts": { purgedAt: NOW - 5_000 } },
    );

    expect(await readFromStore(call(), deps)).toBeNull();
  });

  it("goes to the origin when a tag was purged within the skew before the entry was written", async () => {
    const { deps } = harness(
      { [key("blog/a")]: entry({}, NOW - 10_000) },
      { "r1a2b3c4d|posts": { purgedAt: NOW - 10_500 } },
    );

    expect(await readFromStore(call(), deps)).toBeNull();
  });

  it("serves an entry written after the last purge of its tags", async () => {
    const { deps } = harness(
      { [key("blog/a")]: entry() },
      { "r1a2b3c4d|posts": { purgedAt: NOW - 20_000 } },
    );

    expect((await readFromStore(call(), deps))?.stale).toBe(false);
  });

  it("goes to the origin when the whole release was purged after the entry was written", async () => {
    const { deps } = harness(
      { [key("blog/a")]: entry() },
      { r1a2b3c4d: { purgedAt: NOW - 1_000 } },
    );

    expect(await readFromStore(call(), deps)).toBeNull();
  });

  it("serves an invalidated entry as stale", async () => {
    const { deps } = harness(
      { [key("blog/a")]: entry() },
      { "r1a2b3c4d|_N_T_/blog/a": { invalidatedAt: NOW - 2_000 } },
    );

    const answer = await readFromStore(call(), deps);

    expect(answer?.stale).toBe(true);
  });

  it("serves as fresh an entry the origin rewrote within a second after an invalidation, as the origin judges it", async () => {
    const { deps } = harness(
      { [key("blog/a")]: entry({}, NOW - 10_000) },
      { "r1a2b3c4d|_N_T_/blog/a": { invalidatedAt: NOW - 10_500 } },
    );

    expect((await readFromStore(call(), deps))?.stale).toBe(false);
  });

  it("serves an entry past its revalidate time as stale", async () => {
    const { deps } = harness({ [key("blog/a")]: entry() });

    const answer = await readFromStore(call({ window: { revalidate: 5 } }), deps);

    expect(answer?.stale).toBe(true);
  });

  it("lets Workers Cache serve it while revalidating no longer than until the entry expires", async () => {
    const { deps } = harness({ [key("blog/a")]: entry() });

    const answer = await readFromStore(call({ window: { revalidate: 5, expire: 25 } }), deps);

    expect(answer?.response.headers.get("cache-control")).toBe(
      "s-maxage=0, stale-while-revalidate=15",
    );
  });

  it("goes to the origin for an entry past its expiration", async () => {
    const { deps } = harness({ [key("blog/a")]: entry() });

    expect(await readFromStore(call({ window: { revalidate: 1, expire: 5 } }), deps)).toBeNull();
  });

  it("lets the caller go to the origin when the purge queue cannot be reached", async () => {
    const { deps } = harness({ [key("blog/a")]: entry() }, new Error("queue unreachable"));

    await expect(readFromStore(call(), deps)).rejects.toThrow("queue unreachable");
  });

  it("goes to the origin when a tag of the entry is one Cloudflare cannot store, so a purge could never reach it", async () => {
    const stored = entry({
      headers: { "x-next-cache-tags": "_N_T_/blog/a,café" },
    });
    const { deps } = harness({ [key("blog/a")]: stored });

    expect(await readFromStore(call(), deps)).toBeNull();
  });

  it("sees a purge the purge queue recorded right after an earlier read, however soon", async () => {
    let times: TagTimes = {};
    const { deps } = harness({ [key("blog/a")]: entry() });
    const reading = { ...deps, readTimes: async () => times };

    expect((await readFromStore(call(), reading))?.stale).toBe(false);
    times = { "r1a2b3c4d|posts": { purgedAt: NOW - 1 } };

    expect(await readFromStore(call(), reading)).toBeNull();
  });
});
