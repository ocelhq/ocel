import { describe, expect, it } from "vitest";
import { type ResolveBase, resolveServe, type ServeFetch } from "../src/index";
import { RENDER_RELEASE_HEADER, type RenderRelease, SERVE_REFUSED_HEADER } from "../src/serve-key";
import { serveNodeOrigin } from "../src/serve-node";
import type { ServingPorts } from "../src/serving";
import { answerEveryRecordWith, capturing, FN_URL, makeRecord } from "./origin-deps";
import { fakeServing } from "./serving-deps";

const ORIGIN = "https://abc123.lambda-url.eu-west-2.on.aws/";

const rules = [
  { path: "/blog/*", revalidate: 60, expire: 600 },
  { path: "/", revalidate: 30 },
];

function forwardTo(answer: () => Response = () => new Response("uncached")) {
  const forwarded: Request[] = [];
  return {
    forwarded,
    forward: async (request: Request) => {
      forwarded.push(request);
      return answer();
    },
  };
}

function setup(
  answer?: Parameters<typeof fakeServing>[0],
  uncached?: () => Response,
  over: { rules?: typeof rules } = {},
) {
  const fake = fakeServing(answer);
  const origin = forwardTo(uncached);
  const serve = serveNodeOrigin({
    serving: fake.serving,
    app: "web",
    origin: ORIGIN,
    rules: over.rules ?? rules,
    forward: origin.forward,
  });
  return { ...fake, ...origin, serve };
}

describe("a request a cache rule names", () => {
  it("is answered by Serve under the app's own function URL, never by a direct fetch", async () => {
    const { serve, calls, forwarded } = setup();

    const res = await serve(new Request("https://shop.example.com/blog/a"));

    expect(await res.text()).toBe("page");
    expect(forwarded).toHaveLength(0);
    expect(calls).toHaveLength(1);
    expect(calls[0]!.originUrl).toBe("https://abc123.lambda-url.eu-west-2.on.aws/blog/a");
    expect(calls[0]!.coordinates).toEqual({
      host: "shop.example.com",
      app: "web",
      release: "r0a1b2c3d",
      variant: "",
    });
    expect(calls[0]!.props).toEqual({ kind: "origin" });
  });

  it("asks the origin to shape its response for the edge and names no stored entry", async () => {
    const { serve, calls } = setup();

    await serve(new Request("https://shop.example.com/blog/a"));

    const { request } = calls[0]!;
    expect(request.headers.get("x-ocel-edge-cache")).toBe("1");
    const release = JSON.parse(request.headers.get(RENDER_RELEASE_HEADER)!) as RenderRelease;
    expect(release).toEqual({
      slug: "p1",
      isrPrefix: "prod/p1/web/r0a1b2c3d/isr",
      tags: [],
    });
  });

  it("hands Serve none of the visitor's cookies or credentials", async () => {
    const { serve, calls } = setup();

    await serve(
      new Request("https://shop.example.com/blog/a", {
        headers: { cookie: "session=1", "accept-language": "fr" },
      }),
    );

    expect(calls[0]!.request.headers.get("cookie")).toBeNull();
    expect(calls[0]!.request.headers.get("accept-language")).toBe("fr");
  });

  it("keys the entry by every query parameter, in sorted order", async () => {
    const { serve, calls } = setup();

    await serve(new Request("https://shop.example.com/blog/a?b=2&a=1"));

    expect(calls[0]!.originUrl).toBe("https://abc123.lambda-url.eu-west-2.on.aws/blog/a?a=1&b=2");
  });

  it("tells the visitor how the cache answered, and hides what is for the edge alone", async () => {
    const { serve } = setup(
      () =>
        new Response("page", {
          headers: {
            "cf-cache-status": "HIT",
            "cache-tag": "r0a1b2c3d|posts",
            "cloudflare-cdn-cache-control": "max-age=60",
            "x-ocel-vary": "accept-encoding",
          },
        }),
    );

    const res = await serve(new Request("https://shop.example.com/blog/a"));

    expect(res.headers.get("x-ocel-cache")).toBe("HIT");
    expect(res.headers.get("vary")).toBe("accept-encoding");
    for (const name of ["cache-tag", "cloudflare-cdn-cache-control", "cf-cache-status"]) {
      expect(res.headers.has(name), name).toBe(false);
    }
  });

  it("reports a miss when Serve filled the entry from the origin", async () => {
    const { serve } = setup();

    const res = await serve(new Request("https://shop.example.com/"));

    expect(res.headers.get("x-ocel-cache")).toBe("MISS");
  });

  it("is rendered at the origin uncached when Serve refuses the origin", async () => {
    const { serve, forwarded } = setup(
      () => new Response("no", { status: 503, headers: { [SERVE_REFUSED_HEADER]: "1" } }),
    );

    const res = await serve(new Request("https://shop.example.com/blog/a"));

    expect(await res.text()).toBe("uncached");
    expect(forwarded).toHaveLength(1);
  });
});

describe("a request no cache rule lets the edge cache", () => {
  const uncached = [
    ["a path no rule names", "https://shop.example.com/about", {}],
    ["a write", "https://shop.example.com/blog/a", { method: "POST", body: "x" }],
    ["a HEAD", "https://shop.example.com/blog/a", { method: "HEAD" }],
    [
      "a request with credentials",
      "https://shop.example.com/blog/a",
      { headers: { authorization: "Bearer t" } },
    ],
    ["a path with repeated slashes", "https://shop.example.com/blog//a", {}],
  ] as const;

  for (const [name, url, init] of uncached) {
    it(`goes to the origin uncached for ${name}`, async () => {
      const { serve, calls, forwarded } = setup();

      await serve(new Request(url, init));

      expect(calls).toHaveLength(0);
      expect(forwarded).toHaveLength(1);
    });
  }

  it("goes to the origin uncached when the build states no rules", async () => {
    const { serve, calls, forwarded } = setup(undefined, undefined, { rules: [] });

    await serve(new Request("https://shop.example.com/blog/a"));

    expect(calls).toHaveLength(0);
    expect(forwarded).toHaveLength(1);
  });
});

describe("a purge the origin announces on a response Serve did not cache", () => {
  const announcing = () =>
    new Response("done", {
      headers: { "x-ocel-purge": "purge r0a1b2c3d|posts invalidate r0a1b2c3d|feed" },
    });

  it("is carried out before a write is answered, and hidden from the visitor", async () => {
    const { serve, purges } = setup(undefined, announcing);

    const res = await serve(
      new Request("https://shop.example.com/api/publish", { method: "POST", body: "x" }),
    );

    expect(purges).toEqual([
      {
        groups: [
          { mode: "purge", tags: ["r0a1b2c3d|posts"] },
          { mode: "invalidate", tags: ["r0a1b2c3d|feed"] },
        ],
        awaited: true,
      },
    ]);
    expect(res.headers.has("x-ocel-purge")).toBe(false);
    expect(await res.text()).toBe("done");
  });

  it("is carried out without holding a read back", async () => {
    const { serve, purges } = setup(undefined, announcing);

    await serve(new Request("https://shop.example.com/about"));

    expect(purges.map(({ awaited }) => awaited)).toEqual([false]);
  });
});

describe("a node release resolved by the gateway", () => {
  const ports = (served: Request[]): ServingPorts => ({
    serve: () => ({
      fetch: async (request) => {
        served.push(request);
        return new Response("cached page", { headers: { "cf-cache-status": "HIT" } });
      },
      purge: async () => ({ ok: true }),
    }),
    queue: { raise: async () => {} },
    times: { record: async () => {} },
    waitUntil: () => {},
  });

  async function resolved(over: Parameters<typeof makeRecord>[0], serving?: ServingPorts) {
    const record = makeRecord({ isrPrefix: "prod/p1/api/r0a1b2c3d/isr", ...over });
    const wire = capturing();
    const base: ResolveBase = {
      assetStore: {
        cache: { match: async () => undefined, put: async () => {} },
        waitUntil: () => {},
      },
      originFetch: wire.fetch,
      serving,
    };
    const serve = (await resolveServe(
      {
        binding: answerEveryRecordWith(async () => ({
          kind: "record",
          release: "r0a1b2c3d",
          record,
        })),
        slug: "p1",
        host: "api.example.com",
        app: record.app,
      },
      base,
    )) as ServeFetch;
    return { serve, wire };
  }

  it("answers a path the release's cache rules name from Serve, and reaches the origin for the rest", async () => {
    const served: Request[] = [];
    const { serve, wire } = await resolved(
      { cache: [{ path: "/blog/*", revalidate: 60 }] },
      ports(served),
    );

    const cached = await serve(new Request("https://api.example.com/blog/a"));
    const direct = await serve(new Request("https://api.example.com/about"));

    expect(await cached.text()).toBe("cached page");
    expect(served).toHaveLength(1);
    expect(await direct.text()).toBe("origin");
    expect(wire.calls.map(({ url }) => url)).toEqual([`${FN_URL}about`]);
  });

  it("reaches the origin for every path when the release has no rules", async () => {
    const served: Request[] = [];
    const { serve, wire } = await resolved({}, ports(served));

    await serve(new Request("https://api.example.com/blog/a"));

    expect(served).toHaveLength(0);
    expect(wire.calls).toHaveLength(1);
  });
});
