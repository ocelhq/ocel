import type { NextRouteTable } from "@framework/next-protocol/route-table";
import fixtures from "@framework/next-router/fixtures/image-conformance.json";
import type { ImageConfig } from "@framework/next-router/image";
import { afterEach, describe, expect, it, vi } from "vitest";
import { type ResolveBase, resolveServe, type ServeFetch } from "../src/index";
import type { ReleaseRecord, ReleasesBinding } from "../src/releases";
import { servedNextRelease, UNTOKENED_ISR_PREFIX } from "./next-release";
import { answerEveryRecordWith, capturing } from "./origin-deps";
import { resolveBase } from "./resolve-base";
import { type CountingObjectStore, objectStoreHolding, routeTableKey } from "./route-table-store";
import { fakeServing } from "./serving-deps";

const TABLE: NextRouteTable = {
  rootFunction: "/",
  buildId: "build-1",
  basePath: "",
  pathnames: [],
  routes: {},
  dispatch: {},
  i18n: { locales: ["en", "fr"], defaultLocale: "en" },
};

function makeRecord(over: Partial<ReleaseRecord> = {}): ReleaseRecord {
  return {
    app: "web",
    framework: "next",
    release: "deploy-1",
    buildId: "deploy-1",
    routeTable: { format: "next", key: routeTableKey() },
    functionUrls: { "/": "https://fn.example.com" },
    assetPrefix: "build-1",
    isrPrefix: "prod/p1/web/build-1",
    createdAt: 1_000,
    ...over,
  };
}

function bindingReturning(
  release: string | undefined,
  record: ReleaseRecord | undefined,
): ReleasesBinding {
  return answerEveryRecordWith(async () => {
    if (!release) return { kind: "no-pointer" };
    if (!record) return { kind: "dangling", release };
    return { kind: "record", release, record };
  });
}

function failingBinding(): ReleasesBinding {
  return answerEveryRecordWith(async () => {
    throw new Error("store unreachable");
  });
}

function baseWith(over: Partial<ResolveBase> = {}): ResolveBase {
  return resolveBase({
    routeTableStore: objectStoreHolding({ [routeTableKey()]: TABLE }),
    ...over,
  });
}

function lookup(record: ReleaseRecord, slug = "p1") {
  return {
    binding: bindingReturning("deploy-1", record),
    slug,
    host: "shop.example.com",
    app: "web",
  };
}

describe("a routed release", () => {
  it("refuses a record whose edge bundle lies outside the deployment's own prefix", async () => {
    const record = makeRecord({
      edgeWorkers: {
        bundleKey: "prod/other/web/build-1/edge/bundle.json",
        id: "e1",
        compatDate: "2026-03-10",
      },
    });

    await expect(resolveServe(lookup(record), baseWith())).rejects.toThrow(
      /outside its own prefix/,
    );
  });

  it("accepts a record whose edge bundle sits under the deployment's own prefix", async () => {
    const record = makeRecord({
      edgeWorkers: {
        bundleKey: "prod/p1/web/build-1/edge/bundle.json",
        id: "e1",
        compatDate: "2026-03-10",
      },
    });

    const served = await resolveServe(lookup(record), baseWith());
    expect(served).not.toBeInstanceOf(Response);
  });

  it("forwards a function route to the function URL the record names", async () => {
    const wire = capturing();
    const serve = servedNextRelease({
      table: { dispatch: { "/api": { kind: "function", id: "/" } } },
      record: { functionUrls: { "/": "https://fn.example.com" } },
      fetch: wire.fetch,
    });

    const response = await serve(new Request("https://shop.example.com/api"));

    expect(await response.text()).toBe("origin");
    expect(wire.calls.map(({ url }) => url)).toEqual(["https://fn.example.com/api"]);
  });

  it("reads static pages under the record's asset prefix", async () => {
    const serve = servedNextRelease({
      table: { dispatch: { "/a": { kind: "static" } } },
      record: { assetPrefix: "assets/p1/web/build-1" },
      assets: { "/a.html": "<h1>a</h1>" },
    });

    const response = await serve(new Request("https://shop.example.com/a"));

    expect(response.status).toBe(200);
    expect(await response.text()).toBe("<h1>a</h1>");
  });

  const prerender: NextRouteTable["dispatch"] = {
    "/blog": { kind: "prerender", id: "/blog", config: {} },
  };

  it("serves prerenders through Serve under the release token its ISR prefix names", async () => {
    const served = fakeServing();
    const serve = servedNextRelease({
      table: { dispatch: prerender },
      record: { functionUrls: { "/blog": "https://fn.example.com" } },
      serve: served.serving.serve,
    });

    await serve(new Request("https://shop.example.com/blog"));

    expect(served.calls.map(({ coordinates }) => coordinates.release)).toEqual(["r0a1b2c3d"]);
  });

  it("serves prerenders through Serve under the token, not the build's own name", async () => {
    const served = fakeServing();
    const serve = servedNextRelease({
      table: { dispatch: prerender },
      record: { functionUrls: { "/blog": "https://fn.example.com" }, release: "build-1~abc" },
      serve: served.serving.serve,
    });

    await serve(new Request("https://shop.example.com/blog"));

    expect(served.calls[0].coordinates.release).not.toBe("build-1~abc");
  });

  it("leaves prerenders to the origin when the ISR prefix names no release token", async () => {
    const served = fakeServing();
    const wire = capturing();
    const serve = servedNextRelease({
      table: { dispatch: prerender },
      record: {
        functionUrls: { "/blog": "https://fn.example.com" },
        isrPrefix: UNTOKENED_ISR_PREFIX,
      },
      serve: served.serving.serve,
      fetch: wire.fetch,
    });

    await serve(new Request("https://shop.example.com/blog"));

    expect(served.calls).toHaveLength(0);
    expect(wire.calls.map(({ url }) => url)).toEqual(["https://fn.example.com/blog"]);
  });

  it("returns the baked-in 404 when the app has no active pointer", async () => {
    const served = await resolveServe(
      { binding: bindingReturning(undefined, undefined), slug: "p1", host: "a", app: "web" },
      baseWith(),
    );

    expect(served).toBeInstanceOf(Response);
    const response = served as Response;
    expect(response.status).toBe(404);
    expect(await response.text()).toMatch(/deployment/i);
  });

  it("returns 503 when the store is unreachable on a cold isolate", async () => {
    const served = await resolveServe(
      { binding: failingBinding(), slug: "p1", host: "a", app: "web" },
      baseWith(),
    );

    expect(served).toBeInstanceOf(Response);
    expect((served as Response).status).toBe(503);
  });
});

describe("the route table of a routed deployment", () => {
  afterEach(() => {
    vi.restoreAllMocks();
  });

  function silencedErrors() {
    return vi.spyOn(console, "error").mockImplementation(() => {});
  }

  async function answered(store: CountingObjectStore | undefined, record = makeRecord()) {
    return resolveServe(lookup(record), baseWith({ routeTableStore: store }));
  }

  it("is read from the cache store at the record's key and routes the request", async () => {
    const store = objectStoreHolding({
      [routeTableKey()]: {
        ...TABLE,
        i18n: undefined,
        pathnames: ["/a"],
        routes: {
          beforeMiddleware: [],
          beforeFiles: [],
          afterFiles: [],
          dynamicRoutes: [],
          onMatch: [],
          fallback: [],
        },
        dispatch: { "/a": { kind: "static" } },
      },
    });

    const serving = await resolveServe(
      lookup(makeRecord()),
      baseWith({
        routeTableStore: store,
        assetStore: {
          store: {
            async get(key) {
              if (!key.endsWith("/a.html")) return null;
              return { body: new Blob(["<h1>a</h1>"]).stream() };
            },
          },
          cache: { match: async () => undefined, put: async () => {} },
          waitUntil: () => {},
        },
      }),
    );

    expect(typeof serving).toBe("function");
    const response = await (serving as ServeFetch)(new Request("https://shop.example.com/a"));
    expect(response.status).toBe(200);
    expect(await response.text()).toBe("<h1>a</h1>");
    expect(store.reads).toEqual([routeTableKey()]);
  });

  it("serves a second resolution from the isolate's cache without reading the store again", async () => {
    const store = objectStoreHolding({ [routeTableKey()]: TABLE });

    await answered(store);
    const served = await answered(store);

    expect(typeof served).toBe("function");
    expect(store.reads).toHaveLength(1);
  });

  it("shares one store read between concurrent first resolutions", async () => {
    const store = objectStoreHolding({ [routeTableKey()]: TABLE });

    const [first, second] = await Promise.all([answered(store), answered(store)]);

    expect(typeof first).toBe("function");
    expect(typeof second).toBe("function");
    expect(store.reads).toHaveLength(1);
  });

  it("answers 503 and names the release and key when no cache store is bound", async () => {
    const errors = silencedErrors();

    const served = await answered(undefined);

    expect((served as Response).status).toBe(503);
    expect(errors).toHaveBeenCalledWith(
      expect.stringMatching(new RegExp(`deploy-1.*${routeTableKey()}`)),
      expect.anything(),
    );
  });

  it("answers 503 for a key outside the route tables' shape", async () => {
    const errors = silencedErrors();
    const key = "prod/p1/web/deploy-1/edge/bundle.json";
    const store = objectStoreHolding({ [key]: TABLE });

    const served = await answered(store, makeRecord({ routeTable: { format: "next", key } }));

    expect((served as Response).status).toBe(503);
    expect(store.reads).toEqual([]);
    expect(errors).toHaveBeenCalledWith(
      expect.stringMatching(new RegExp(`deploy-1.*${key}`)),
      expect.anything(),
    );
  });

  it("refuses a key belonging to another project's route tables", async () => {
    const errors = silencedErrors();
    const key = routeTableKey("other");
    const store = objectStoreHolding({ [key]: TABLE });

    const served = await answered(store, makeRecord({ routeTable: { format: "next", key } }));

    expect((served as Response).status).toBe(503);
    expect(store.reads).toEqual([]);
    expect(errors).toHaveBeenCalledWith(
      expect.stringMatching(new RegExp(`deploy-1.*${key}`)),
      expect.anything(),
    );
  });

  it("answers 503 when no route table is stored at the key", async () => {
    const errors = silencedErrors();

    const served = await answered(objectStoreHolding({}));

    expect((served as Response).status).toBe(503);
    expect(errors).toHaveBeenCalledWith(
      expect.stringMatching(new RegExp(`deploy-1.*${routeTableKey()}`)),
      expect.anything(),
    );
  });

  it("answers 503 when the stored route table is not JSON", async () => {
    const errors = silencedErrors();

    const served = await answered(objectStoreHolding({ [routeTableKey()]: "{not json" }));

    expect((served as Response).status).toBe(503);
    expect(errors).toHaveBeenCalledWith(
      expect.stringMatching(new RegExp(`deploy-1.*${routeTableKey()}`)),
      expect.anything(),
    );
  });

  it("reads the store again after a failed read", async () => {
    silencedErrors();
    const store = objectStoreHolding({});
    await answered(store);
    store.objects.set(routeTableKey(), JSON.stringify(TABLE));

    const served = await answered(store);

    expect(typeof served).toBe("function");
    expect(store.reads).toHaveLength(2);
  });
});

describe("the image origin of a routed deployment", () => {
  const entryUrl = "https://r1-x.o.example.com";
  const images = (fixtures.variants as unknown as Array<{ config: ImageConfig }>)[0].config;

  function reached(fetched: string[]): typeof fetch {
    return (async (input: RequestInfo | URL) => {
      fetched.push(String(input));
      return new Response("bytes", { status: 200, headers: { "content-type": "image/webp" } });
    }) as typeof fetch;
  }

  function imageRelease(base: Partial<ResolveBase>) {
    return servedNextRelease({
      table: { rootFunction: "bundle-0", images },
      record: { functionUrls: { "bundle-0": entryUrl }, isrPrefix: UNTOKENED_ISR_PREFIX },
      base,
    });
  }

  const imageRequest = () =>
    new Request("https://shop.example.com/_next/image?url=%2Fa.png&w=640&q=75", {
      headers: { accept: "image/webp" },
    });

  it("asks the deployment's own service for an image when no optimizer is bound", async () => {
    const fetched: string[] = [];
    const serve = imageRelease({ imagesAtDeployment: true, originFetch: reached(fetched) });

    const response = await serve(imageRequest());

    expect(response.status).toBe(200);
    expect(fetched).toEqual(["https://r1-x.o.example.com/_ocel/image"]);
  });

  it("keeps the AWS optimizer when one is bound", async () => {
    const optimizer = async () => new Response("optimizer", { status: 200 });
    const serve = imageRelease({ imagesAtDeployment: false, imageOrigin: optimizer });

    const response = await serve(imageRequest());

    expect(await response.text()).toBe("optimizer");
  });

  it("leaves an origin reached with AWS keys without a deployment image origin", async () => {
    const fetched: string[] = [];
    const serve = imageRelease({ originFetch: reached(fetched) });

    const response = await serve(imageRequest());

    expect(response.status).toBe(502);
    expect(fetched).toEqual([]);
  });

  it("answers unprovisioned when the deployment's service cannot be reached", async () => {
    const serve = imageRelease({
      imagesAtDeployment: true,
      originFetch: (async () => {
        throw new Error("unreachable");
      }) as typeof fetch,
    });

    const response = await serve(imageRequest());

    expect(response.status).toBe(502);
    expect(await response.text()).toContain("No image optimizer is provisioned");
  });
});
