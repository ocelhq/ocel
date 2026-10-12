import { invalidatesByCacheTag, servesOnlyFresh } from "@framework/node-runtime/host";
import { expect, test } from "vitest";
import {
  type OriginShaping,
  originShaping,
  releaseOf,
  revalidatingRoutes,
  shapeOriginCache,
} from "../src/cache-shaping.mjs";
import { noteTags } from "../src/origin-tags.mjs";
import type { ProjectManifest } from "../src/project-manifest.mjs";

const prefix = "prod/shop/web/r0a1b2c3d/isr";

function manifest(
  prerender: Record<string, unknown> = {},
  config: Record<string, unknown> = {},
): ProjectManifest {
  return { config, distDir: "/app/.next", prerender };
}

function fakeRes(headers: Record<string, string> = {}, statusCode = 200) {
  const sent: Record<string, string> = { ...headers };
  return {
    statusCode,
    headersSent: false,
    headers: sent,
    getHeader: (name: string) => sent[name],
    setHeader: (name: string, value: string) => {
      sent[name] = value;
    },
    writeHead(this: any) {
      this.headersSent = true;
      return this;
    },
  };
}

function serve(
  shaping: OriginShaping,
  url: string,
  res: ReturnType<typeof fakeRes>,
  opts: { tags?: string[]; method?: string; headers?: Record<string, string> } = {},
): Record<string, string> {
  const req: any = { url, method: opts.method ?? "GET", headers: { ...opts.headers } };
  shapeOriginCache(req, res as any, shaping);
  if (opts.tags) noteTags(req.headers, opts.tags);
  res.writeHead();
  return res.headers;
}

const isrRoutes = {
  routes: {
    "/isr": { initialRevalidateSeconds: 60, initialExpireSeconds: 660 },
    "/never": { initialRevalidateSeconds: false },
    "/zero": { initialRevalidateSeconds: 0 },
  },
  dynamicRoutes: {
    "/blog/[slug]": { routeRegex: "^/blog/([^/]+?)(?:/)?$", fallbackRevalidate: 120 },
    "/bad/[slug]": { routeRegex: "^/bad/([^/]+?(", fallbackRevalidate: 30 },
  },
};

function shaping(
  env: Record<string, string> = {},
  config: Record<string, unknown> = {},
  tagsPerObject?: number,
  extra: Record<string, string> = {},
) {
  return originShaping(
    revalidatingRoutes(manifest(isrRoutes, config)),
    {
      OCEL_EDGE_CACHE: "all",
      OCEL_CACHE_TAG_PURGE: "1",
      OCEL_ISR_PREFIX: prefix,
      ...env,
      ...extra,
    } as NodeJS.ProcessEnv,
    tagsPerObject,
  )!;
}

test("the gate stays shut when no edge in front caches responses", () => {
  const routes = revalidatingRoutes(manifest(isrRoutes));
  expect(originShaping(routes, {} as any)).toBeNull();
  expect(originShaping(routes, { OCEL_EDGE_CACHE: "" } as any)).toBeNull();
});

test("shapes nothing on a host that dispatches paths but has no cache in front", () => {
  const routes = revalidatingRoutes(manifest(isrRoutes));
  expect(
    originShaping(routes, { OCEL_ORIGIN_DISPATCH: "1", OCEL_CACHE_TAG_PURGE: "1" } as any),
  ).toBeNull();
});

test("the gate opens for a function behind a cache that dispatches no path itself", () => {
  const routes = revalidatingRoutes(manifest(isrRoutes));
  expect(originShaping(routes, { OCEL_EDGE_CACHE: "all" } as any)).not.toBeNull();
  expect(originShaping(routes, { OCEL_EDGE_CACHE: "marked" } as any)).not.toBeNull();
});

test("tags and shapes every response of a non-root function behind a cache that stores them all", () => {
  const html = { "content-type": "text/html; charset=utf-8" };
  const nonRoot = shaping({}, {}, undefined, { OCEL_ORIGIN_DISPATCH: "" });

  const headers = serve(nonRoot, "/isr", fakeRes(html), { tags: ["products"] });

  expect(headers["cache-tag"]).toBe("r0a1b2c3d|products");
  expect(headers["cache-control"]).toBe("s-maxage=60, stale-while-revalidate=600");
});

test("behind an edge that stores only what it marks, shapes only a request that asked for the edge cache", () => {
  const edgeShaping = shaping({ OCEL_EDGE_CACHE: "marked" });
  const html = { "content-type": "text/html; charset=utf-8" };

  const plain = serve(edgeShaping, "/isr", fakeRes(html), { tags: ["products"] });
  const asked = serve(edgeShaping, "/isr", fakeRes(html), {
    tags: ["products"],
    headers: { "x-ocel-edge-cache": "1" },
  });

  expect(plain["cache-control"]).toBeUndefined();
  expect(plain["cache-tag"]).toBeUndefined();
  expect(plain["x-ocel-cache-tag"]).toBeUndefined();
  expect(asked["cache-control"]).toBe("s-maxage=60, stale-while-revalidate=600");
});

test("names the tags of a request that asked for the edge cache in x-ocel-cache-tag, since Cloudflare strips cache-tag from what a worker fetches", () => {
  const html = { "content-type": "text/html; charset=utf-8" };

  const asked = serve(shaping({ OCEL_EDGE_CACHE: "marked" }), "/isr", fakeRes(html), {
    tags: ["products"],
    headers: { "x-ocel-edge-cache": "1" },
  });

  expect(asked["x-ocel-cache-tag"]).toBe("r0a1b2c3d|products");
  expect(asked["cache-tag"]).toBeUndefined();
});

test("only a front the deploy declared tag-purging is given cache tags", () => {
  expect(invalidatesByCacheTag({ OCEL_CACHE_TAG_PURGE: "1" } as NodeJS.ProcessEnv)).toBe(true);
  expect(invalidatesByCacheTag({} as NodeJS.ProcessEnv)).toBe(false);
  expect(invalidatesByCacheTag({ OCEL_CACHE_TAG_PURGE: "" } as NodeJS.ProcessEnv)).toBe(false);
});

test("only an origin behind an edge cache serves nothing stale", () => {
  expect(servesOnlyFresh({ OCEL_EDGE_CACHE: "all" } as NodeJS.ProcessEnv)).toBe(true);
  expect(servesOnlyFresh({ OCEL_EDGE_CACHE: "marked" } as NodeJS.ProcessEnv)).toBe(true);
  expect(servesOnlyFresh({} as NodeJS.ProcessEnv)).toBe(false);
  expect(servesOnlyFresh({ OCEL_EDGE_CACHE: "" } as NodeJS.ProcessEnv)).toBe(false);
});

test("leaves the tag header off a front that invalidates by nothing", () => {
  const headers = serve(shaping({ OCEL_CACHE_TAG_PURGE: "" }), "/isr", fakeRes(), {
    tags: ["products"],
  });

  expect(headers["cache-tag"]).toBeUndefined();
});

test("reads the release out of the isr prefix", () => {
  expect(releaseOf(prefix)).toBe("r0a1b2c3d");
});

test("refuses a release from a prefix that is absent or shaped differently", () => {
  expect(releaseOf(undefined)).toBeNull();
  expect(releaseOf("")).toBeNull();
  expect(releaseOf("prod/shop/web/r0a1b2c3d")).toBeNull();
  expect(releaseOf("prod/shop/web/r0a1b2c3d/bytecode")).toBeNull();
  expect(releaseOf("prod/shop/web/deadbeef/isr")).toBeNull();
  expect(releaseOf("prod/shop/web/rZZZZZZZZ/isr")).toBeNull();
});

test("leaves the tag header off when the prefix names no release", () => {
  const headers = serve(shaping({ OCEL_ISR_PREFIX: "" }), "/isr", fakeRes(), {
    tags: ["products"],
  });

  expect(headers["cache-tag"]).toBeUndefined();
});

test("shapes s-maxage from the manifest for a route it revalidates", () => {
  const headers = serve(shaping(), "/isr", fakeRes({ "content-type": "text/html; charset=utf-8" }));

  expect(headers["cache-control"]).toBe("s-maxage=60, stale-while-revalidate=600");
});

test("omits stale-while-revalidate rather than emitting it without a delta", () => {
  const headers = serve(
    shaping(),
    "/blog/hello",
    fakeRes({ "content-type": "text/html; charset=utf-8" }),
  );

  expect(headers["cache-control"]).toBe("s-maxage=120");
});

test("matches a route the request trailing-slashed and queried", () => {
  const headers = serve(
    shaping(),
    "/isr/?ref=email",
    fakeRes({ "content-type": "text/html; charset=utf-8" }),
  );

  expect(headers["cache-control"]).toBe("s-maxage=60, stale-while-revalidate=600");
});

test("matches a route reached under the app's basePath", () => {
  const headers = serve(
    shaping({}, { basePath: "/shop" }),
    "/shop/isr",
    fakeRes({ "content-type": "text/html; charset=utf-8" }),
  );

  expect(headers["cache-control"]).toBe("s-maxage=60, stale-while-revalidate=600");
});

test("matches a route reached through its data route", () => {
  const headers = serve(
    shaping(),
    "/_next/data/BUILD/isr.json",
    fakeRes({ "content-type": "text/html; charset=utf-8" }),
  );

  expect(headers["cache-control"]).toBe("s-maxage=60, stale-while-revalidate=600");
});

test("leaves a route the manifest never revalidates alone", () => {
  for (const url of ["/never", "/zero", "/unknown", "/bad/thing"]) {
    const headers = serve(shaping(), url, fakeRes({ "content-type": "text/html; charset=utf-8" }));
    expect(headers["cache-control"]).toBeUndefined();
  }
});

test("refuses to shape a response Next declared personal", () => {
  for (const declared of ["private, max-age=0", "no-store", "no-cache"]) {
    const headers = serve(
      shaping(),
      "/isr",
      fakeRes({ "content-type": "text/html; charset=utf-8", "cache-control": declared }),
    );
    expect(headers["cache-control"]).toBe(declared);
  }
});

test("refuses to overwrite an s-maxage Next already chose", () => {
  const headers = serve(
    shaping(),
    "/isr",
    fakeRes({
      "content-type": "text/html; charset=utf-8",
      "cache-control": "s-maxage=5",
    }),
  );

  expect(headers["cache-control"]).toBe("s-maxage=5");
});

test("refuses to shape a response that is not html", () => {
  const headers = serve(shaping(), "/isr", fakeRes({ "content-type": "text/x-component" }));

  expect(headers["cache-control"]).toBeUndefined();
});

test("refuses to shape an error, a cookie-bearing response, or a mutation", () => {
  const html = { "content-type": "text/html; charset=utf-8" };
  expect(serve(shaping(), "/isr", fakeRes(html, 500))["cache-control"]).toBeUndefined();
  expect(
    serve(shaping(), "/isr", fakeRes({ ...html, "set-cookie": "sid=1" }))["cache-control"],
  ).toBeUndefined();
  expect(
    serve(shaping(), "/isr", fakeRes(html), { method: "POST" })["cache-control"],
  ).toBeUndefined();
});

test("leaves the window of a response Next calls stale as Next set it", () => {
  const headers = serve(
    shaping(),
    "/isr",
    fakeRes({
      "content-type": "text/html; charset=utf-8",
      "x-nextjs-cache": "STALE",
      "cache-control": "s-maxage=60, stale-while-revalidate=600",
    }),
  );

  expect(headers["cache-control"]).toBe("s-maxage=60, stale-while-revalidate=600");
});

test("leaves a hit on its own window", () => {
  const headers = serve(
    shaping(),
    "/isr",
    fakeRes({
      "content-type": "text/html; charset=utf-8",
      "x-nextjs-cache": "HIT",
      "cache-control": "s-maxage=60, stale-while-revalidate=600",
    }),
  );

  expect(headers["cache-control"]).toBe("s-maxage=60, stale-while-revalidate=600");
});

test("tags an rsc response too, so a purge reaches both variants", () => {
  const headers = serve(shaping(), "/isr", fakeRes({ "content-type": "text/x-component" }), {
    tags: ["products", "_N_T_/products"],
  });

  expect(headers["cache-tag"]).toBe("r0a1b2c3d|_N_T_/products,r0a1b2c3d|products");
});

test("stamps the soft tags first, since the front keeps only as many as its host declares", () => {
  const many = Array.from({ length: 60 }, (_, i) => `t${i}`);
  const headers = serve(
    shaping({}, {}, 50),
    "/isr",
    fakeRes({ "content-type": "text/x-component" }),
    { tags: [...many, "_N_T_/products"] },
  );

  const stored = headers["cache-tag"]!.split(",");
  expect(stored).toHaveLength(50);
  expect(stored[0]).toBe("r0a1b2c3d|_N_T_/products");
  expect(stored.at(-1)).toBe("r0a1b2c3d|t48");
});

test("stamps every tag when the host declares no limit per object", () => {
  const many = Array.from({ length: 60 }, (_, i) => `t${i}`);
  const headers = serve(shaping(), "/isr", fakeRes({ "content-type": "text/x-component" }), {
    tags: many,
  });

  expect(headers["cache-tag"]!.split(",")).toHaveLength(60);
});

test("leaves out a tag the front could never store", () => {
  const headers = serve(shaping(), "/isr", fakeRes({ "content-type": "text/x-component" }), {
    tags: ["with,comma", "x".repeat(256), "kept"],
  });

  expect(headers["cache-tag"]).toBe("r0a1b2c3d|kept");
});
