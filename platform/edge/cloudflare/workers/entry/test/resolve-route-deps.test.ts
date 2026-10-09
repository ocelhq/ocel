import type { NextRouteTable } from "@framework/next-protocol/route-table";
import type { AssetBucket } from "@framework/next-router/assets";
import { afterEach, describe, expect, it, vi } from "vitest";
import {
  type ResolveBase,
  type RouteDeps,
  resolveRouteDeps,
  resolveServe,
  type ServeFetch,
} from "../src/index";
import type { ReleaseRecord, ReleasesBinding } from "../src/releases";
import { answerEveryRecordWith } from "./origin-deps";
import { type CountingObjectStore, objectStoreHolding, routeTableKey } from "./route-table-store";

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

const assetStore: RouteDeps["assetStore"] = {
  cache: { match: async () => undefined, put: async () => {} },
  waitUntil: () => {},
};

function baseWith(over: Partial<ResolveBase> = {}): ResolveBase {
  return {
    assetStore,
    routeTableStore: objectStoreHolding({ [routeTableKey()]: TABLE }),
    ...over,
  };
}

function lookup(record: ReleaseRecord, slug = "p1") {
  return { binding: bindingReturning("deploy-1", record), slug, app: "web" };
}

describe("resolveRouteDeps", () => {
  it("refuses a record whose edge bundle lies outside the deployment's own prefix", async () => {
    const record = makeRecord({
      edgeWorkers: {
        bundleKey: "prod/other/web/build-1/edge/bundle.json",
        id: "e1",
        compatDate: "2026-03-10",
      },
    });

    await expect(resolveRouteDeps(lookup(record), baseWith())).rejects.toThrow(
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

    const deps = await resolveRouteDeps(lookup(record), baseWith());
    expect(deps).not.toBeInstanceOf(Response);
  });

  it("wires the resolved Deployment's route table and functionUrls into RouteDeps", async () => {
    const record = makeRecord();
    const deps = await resolveRouteDeps(lookup(record), baseWith());

    expect(deps).not.toBeInstanceOf(Response);
    const routeDeps = deps as RouteDeps;
    expect(routeDeps.manifest).toEqual(TABLE);
    expect(routeDeps.functionUrls).toEqual(record.functionUrls);
    expect(routeDeps.appBuildId).toBe("deploy-1");
  });

  it("fills the asset store's prefix from the record's asset prefix", async () => {
    const record = makeRecord({ assetPrefix: "assets/p1/web/build-1" });
    const deps = await resolveRouteDeps(lookup(record), baseWith());

    expect(deps).not.toBeInstanceOf(Response);
    expect((deps as RouteDeps).assetStore.assetPrefix).toBe("assets/p1/web/build-1");
  });

  it("fills the interception config's ISR prefix from the record's ISR prefix", async () => {
    const record = makeRecord({ isrPrefix: "prod/p1/web/build-1" });
    const store = { get: async () => null };
    const deps = await resolveRouteDeps(lookup(record), baseWith({ interception: { store } }));

    expect(deps).not.toBeInstanceOf(Response);
    expect((deps as RouteDeps).interception?.config).toEqual({
      isrPrefix: "prod/p1/web/build-1",
    });
  });

  it("leaves interception undefined when no cache store is bound", async () => {
    const deps = await resolveRouteDeps(lookup(makeRecord()), baseWith());

    expect((deps as RouteDeps).interception).toBeUndefined();
  });

  it("returns the baked-in 404 when the app has no active pointer", async () => {
    const deps = await resolveRouteDeps(
      { binding: bindingReturning(undefined, undefined), slug: "p1", app: "web" },
      baseWith(),
    );

    expect(deps).toBeInstanceOf(Response);
    const response = deps as Response;
    expect(response.status).toBe(404);
    expect(await response.text()).toMatch(/deployment/i);
  });

  it("returns 501 for a Deployment that ships no route table", async () => {
    const record = makeRecord({ framework: "node", routeTable: undefined });
    const deps = await resolveRouteDeps(lookup(record), baseWith());

    expect(deps).toBeInstanceOf(Response);
    const response = deps as Response;
    expect(response.status).toBe(501);
    expect(await response.text()).toMatch(/node/);
  });

  it("returns 503 when the store is unreachable on a cold isolate", async () => {
    const deps = await resolveRouteDeps(
      { binding: failingBinding(), slug: "p1", app: "web" },
      baseWith(),
    );

    expect(deps).toBeInstanceOf(Response);
    expect((deps as Response).status).toBe(503);
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
    return resolveRouteDeps(lookup(record), baseWith({ routeTableStore: store }));
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
    const assets: AssetBucket = {
      async get(key) {
        if (!key.endsWith("/a.html")) return null;
        return { body: new Blob(["<h1>a</h1>"]).stream() };
      },
    };

    const serving = await resolveServe(
      { ...lookup(makeRecord()), host: "shop.example.com" },
      baseWith({ routeTableStore: store, assetStore: { ...assetStore, store: assets } }),
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
    const deps = await answered(store);

    expect((deps as RouteDeps).manifest).toEqual(TABLE);
    expect(store.reads).toHaveLength(1);
  });

  it("shares one store read between concurrent first resolutions", async () => {
    const store = objectStoreHolding({ [routeTableKey()]: TABLE });

    const [first, second] = await Promise.all([answered(store), answered(store)]);

    expect((first as RouteDeps).manifest).toEqual(TABLE);
    expect((second as RouteDeps).manifest).toEqual(TABLE);
    expect(store.reads).toHaveLength(1);
  });

  it("answers 503 and names the release and key when no cache store is bound", async () => {
    const errors = silencedErrors();

    const deps = await answered(undefined);

    expect((deps as Response).status).toBe(503);
    expect(errors).toHaveBeenCalledWith(
      expect.stringMatching(new RegExp(`deploy-1.*${routeTableKey()}`)),
      expect.anything(),
    );
  });

  it("answers 503 for a key outside the route tables' shape", async () => {
    const errors = silencedErrors();
    const key = "prod/p1/web/deploy-1/edge/bundle.json";
    const store = objectStoreHolding({ [key]: TABLE });

    const deps = await answered(store, makeRecord({ routeTable: { format: "next", key } }));

    expect((deps as Response).status).toBe(503);
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

    const deps = await answered(store, makeRecord({ routeTable: { format: "next", key } }));

    expect((deps as Response).status).toBe(503);
    expect(store.reads).toEqual([]);
    expect(errors).toHaveBeenCalledWith(
      expect.stringMatching(new RegExp(`deploy-1.*${key}`)),
      expect.anything(),
    );
  });

  it("answers 503 when no route table is stored at the key", async () => {
    const errors = silencedErrors();

    const deps = await answered(objectStoreHolding({}));

    expect((deps as Response).status).toBe(503);
    expect(errors).toHaveBeenCalledWith(
      expect.stringMatching(new RegExp(`deploy-1.*${routeTableKey()}`)),
      expect.anything(),
    );
  });

  it("answers 503 when the stored route table is not JSON", async () => {
    const errors = silencedErrors();

    const deps = await answered(objectStoreHolding({ [routeTableKey()]: "{not json" }));

    expect((deps as Response).status).toBe(503);
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

    const deps = await answered(store);

    expect((deps as RouteDeps).manifest).toEqual(TABLE);
    expect(store.reads).toHaveLength(2);
  });
});

describe("the image origin of a routed deployment", () => {
  const entryUrl = "https://r1-x.o.example.com";

  function imageRecord(): ReleaseRecord {
    return makeRecord({ functionUrls: { "bundle-0": entryUrl } });
  }

  const imageTableStore = objectStoreHolding({
    [routeTableKey()]: { ...TABLE, rootFunction: "bundle-0" },
  });

  const payload = {
    assetPrefix: "build-1",
    url: "/a.png",
    w: 640,
    q: 75,
    accept: "image/webp",
    mimeType: "image/webp",
    configHash: "deadbeef",
  };

  function reached(fetched: string[]): typeof fetch {
    return (async (input: RequestInfo | URL) => {
      fetched.push(String(input));
      return new Response("bytes", { status: 200, headers: { "content-type": "image/webp" } });
    }) as typeof fetch;
  }

  async function depsWith(base: Partial<ResolveBase>) {
    const deps = await resolveRouteDeps(
      lookup(imageRecord()),
      baseWith({ routeTableStore: imageTableStore, ...base }),
    );
    return deps as RouteDeps;
  }

  it("asks the deployment's own service for an image when no optimizer is bound", async () => {
    const fetched: string[] = [];
    const deps = await depsWith({ imagesAtDeployment: true, originFetch: reached(fetched) });

    const response = await deps.imageOrigin!(payload);

    expect(response.status).toBe(200);
    expect(fetched).toEqual(["https://r1-x.o.example.com/_ocel/image"]);
  });

  it("keeps the AWS optimizer when one is bound", async () => {
    const optimizer = async () => new Response("optimizer", { status: 200 });
    const deps = await depsWith({ imagesAtDeployment: false, imageOrigin: optimizer });

    expect(deps.imageOrigin).toBe(optimizer);
  });

  it("leaves an origin reached with AWS keys without a deployment image origin", async () => {
    const deps = await depsWith({ originFetch: reached([]) });

    expect(deps.imageOrigin).toBeUndefined();
  });

  it("answers unprovisioned when the deployment's service cannot be reached", async () => {
    const deps = await depsWith({
      imagesAtDeployment: true,
      originFetch: (async () => {
        throw new Error("unreachable");
      }) as typeof fetch,
    });

    const response = await deps.imageOrigin!(payload);

    expect(response.status).toBe(502);
    expect(await response.text()).toContain("No image optimizer is provisioned");
  });
});
