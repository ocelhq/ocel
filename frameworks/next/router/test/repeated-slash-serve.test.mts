import type { Route } from "@next/routing";
import { describe, expect, it } from "vitest";

import { serve, type RouteDeps } from "../src/index.mjs";
import type { AssetBucket } from "../src/assets.mjs";

function assetStoreServing(files: Record<string, string>): RouteDeps["assetStore"] {
  const store: AssetBucket = {
    async get(key) {
      const body = files[key];
      if (body === undefined) return null;
      return { body: new Blob([body]).stream() };
    },
  };
  return {
    store,
    assetPrefix: "",
    basePath: "",
    cache: { match: async () => undefined, put: async () => {} },
    waitUntil: () => {},
  };
}

function deps(basePath = ""): RouteDeps {
  return {
    manifest: {
      entry: "",
      buildId: "t",
      basePath,
      trailingSlash: false,
      pathnames: ["/a"],
      routes: {
        beforeMiddleware: [],
        beforeFiles: [],
        afterFiles: [],
        dynamicRoutes: [],
        onMatch: [],
        fallback: [],
      } as unknown as RouteDeps["manifest"]["routes"],
      dispatch: { "/a": { kind: "static" as const } },
    },
    functionUrls: {},
    slug: "p1",
    deploymentId: "d1",
    app: "web",
    assetStore: assetStoreServing({ "/a.html": "<h1>a</h1>", "/404.html": "not found" }),
  };
}

function get(rawPath: string) {
  return new Request(`https://app.example${rawPath}`, { redirect: "manual" });
}

describe("repeated slashes and backslashes", () => {
  it("308s a repeated slash to its collapsed form", async () => {
    const res = await serve(get("/a//b"), deps());
    expect(res.status).toBe(308);
    expect(res.headers.get("location")).toBe("/a/b");
  });

  it("308s a repeated slash under a basePath", async () => {
    const res = await serve(get("/basepath//en/x"), deps());
    expect(res.status).toBe(308);
    expect(res.headers.get("location")).toBe("/basepath/en/x");
  });

  it("preserves the query string verbatim, including a further '?'", async () => {
    const res = await serve(get("/a//b?x=1//2"), deps());
    expect(res.status).toBe(308);
    expect(res.headers.get("location")).toBe("/a/b?x=1//2");
  });

  it("collapses a run of more than two slashes", async () => {
    const res = await serve(get("/a///b"), deps());
    expect(res.status).toBe(308);
    expect(res.headers.get("location")).toBe("/a/b");
  });

  it("leaves a clean path alone and falls through to normal routing", async () => {
    const res = await serve(get("/a"), deps());
    expect(res.status).toBe(200);
    expect(await res.text()).toBe("<h1>a</h1>");
  });
});
