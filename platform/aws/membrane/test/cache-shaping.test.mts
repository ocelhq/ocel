import { expect, test } from "vitest";
import {
  originShaping,
  releaseOf,
  shapeOriginCache,
  type OriginShaping,
} from "../src/next/cache-shaping.mjs";
import { invalidatesByCacheTag, routerMode } from "../src/shared/edge-kind.mjs";
import { noteTags } from "../src/next/origin-tags.mjs";
import type { ProjectManifest } from "../src/next/project-manifest.mjs";

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
  opts: { tags?: string[]; method?: string } = {},
): Record<string, string> {
  const req: any = { url, method: opts.method ?? "GET", headers: {} };
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

function shaping(env: Record<string, string> = {}, config: Record<string, unknown> = {}) {
  return originShaping(manifest(isrRoutes, config), {
    OCEL_ORIGIN_ROUTER: "1",
    OCEL_CACHE_TAG_PURGE: "1",
    OCEL_ISR_PREFIX: prefix,
    ...env,
  } as NodeJS.ProcessEnv)!;
}

test("the gate stays shut when the deploy declared no origin router", () => {
  expect(routerMode({} as NodeJS.ProcessEnv)).toBe(false);
  expect(routerMode({ OCEL_ORIGIN_ROUTER: "" } as NodeJS.ProcessEnv)).toBe(false);
  expect(originShaping(manifest(isrRoutes), {} as any)).toBeNull();
});

test("the gate opens when the deploy declared the origin hosts the router", () => {
  expect(routerMode({ OCEL_ORIGIN_ROUTER: "1" } as NodeJS.ProcessEnv)).toBe(true);
  expect(
    originShaping(manifest(isrRoutes), { OCEL_ORIGIN_ROUTER: "1" } as any),
  ).not.toBeNull();
});

test("only a front the deploy declared tag-purging is given cache tags", () => {
  expect(invalidatesByCacheTag({ OCEL_CACHE_TAG_PURGE: "1" } as NodeJS.ProcessEnv)).toBe(true);
  expect(invalidatesByCacheTag({} as NodeJS.ProcessEnv)).toBe(false);
  expect(invalidatesByCacheTag({ OCEL_CACHE_TAG_PURGE: "" } as NodeJS.ProcessEnv)).toBe(false);
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
  const headers = serve(
    shaping(),
    "/isr",
    fakeRes({ "content-type": "text/html; charset=utf-8" }),
  );

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
    const headers = serve(
      shaping(),
      url,
      fakeRes({ "content-type": "text/html; charset=utf-8" }),
    );
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

test("clamps a stale serve without promoting it to public", () => {
  const headers = serve(
    shaping(),
    "/isr",
    fakeRes({
      "content-type": "text/html; charset=utf-8",
      "x-nextjs-cache": "STALE",
      "cache-control": "s-maxage=60, stale-while-revalidate=600",
    }),
  );

  expect(headers["cache-control"]).toBe("s-maxage=0, must-revalidate");
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

test("stamps the soft tags first, since CloudFront keeps only the first fifty", () => {
  const many = Array.from({ length: 60 }, (_, i) => `t${i}`);
  const headers = serve(shaping(), "/isr", fakeRes({ "content-type": "text/x-component" }), {
    tags: [...many, "_N_T_/products"],
  });

  const stored = headers["cache-tag"]!.split(",");
  expect(stored).toHaveLength(50);
  expect(stored[0]).toBe("r0a1b2c3d|_N_T_/products");
  expect(stored.at(-1)).toBe("r0a1b2c3d|t48");
});

test("leaves out a tag CloudFront could never store", () => {
  const headers = serve(shaping(), "/isr", fakeRes({ "content-type": "text/x-component" }), {
    tags: ["with,comma", "x".repeat(256), "kept"],
  });

  expect(headers["cache-tag"]).toBe("r0a1b2c3d|kept");
});
