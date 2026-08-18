import { describe, expect, it } from "vitest";

import {
  EDGE_HEADER,
  resolveServe,
  serve,
  type ResolveBase,
  type RouteDeps,
  type ServeFetch,
} from "../src/index";
import type { AssetBucket } from "@framework/next-router/assets";
import type {
  DeploymentRecord,
  DeploymentsBinding,
  PointerRecordResult,
} from "../src/deployments";

function routedDeps(): RouteDeps {
  const store: AssetBucket = {
    async get(key) {
      const body: string | undefined = { "/a.html": "<h1>a</h1>" }[key];
      if (body === undefined) return null;
      return { body: new Blob([body]).stream() };
    },
  };
  return {
    manifest: {
      buildId: "t",
      basePath: "",
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
    assetStore: {
      store,
      assetPrefix: "",
      basePath: "",
      cache: { match: async () => undefined, put: async () => {} },
      waitUntil: () => {},
    },
  };
}

const FN_URL = "https://abc123.lambda-url.eu-west-2.on.aws/";

const originRecord: DeploymentRecord = {
  app: "api",
  framework: "express",
  buildId: "0123456789abcdef",
  routingManifest: null,
  functionUrls: { api: FN_URL },
  assetPrefix: "",
  isrPrefix: "",
  createdAt: 1_000,
};

function bindingReturning(result: PointerRecordResult): DeploymentsBinding {
  return { async pointerRecord() { return result; } };
}

const base: ResolveBase = {
  assetStore: {
    cache: { match: async () => undefined, put: async () => {} },
    waitUntil: () => {},
  },
};

async function resolved(binding: DeploymentsBinding, host: string) {
  return resolveServe(
    { binding, slug: "p1", host, app: "api" },
    { ...base, fetch: (async () => new Response("origin")) as typeof fetch },
  );
}

describe("the edge marks every response as its own", () => {
  it("marks the bootstrap placeholder a bound host lands on", async () => {
    const served = await resolved(
      bindingReturning({ kind: "no-pointer" }),
      "bootstrap.example.com",
    );

    expect(served).toBeInstanceOf(Response);
    expect((served as Response).status).toBe(404);
    expect((served as Response).headers.get(EDGE_HEADER)).toBe("cloudflare");
  });

  it("marks the response of a store that cannot answer", async () => {
    const served = await resolved(
      bindingReturning({ kind: "dangling", identity: "b1" }),
      "dangling.example.com",
    );

    expect(served).toBeInstanceOf(Response);
    expect((served as Response).status).toBe(503);
    expect((served as Response).headers.get(EDGE_HEADER)).toBe("cloudflare");
  });

  it("marks what the router serves", async () => {
    const response = await serve(
      new Request("https://shop.example.com/a"),
      routedDeps(),
    );

    expect(response.status).toBe(200);
    expect(response.headers.get(EDGE_HEADER)).toBe("cloudflare");
  });

  it("marks what an origin-served deployment answers", async () => {
    const served = await resolved(
      bindingReturning({ kind: "record", identity: "b1", record: originRecord }),
      "origin.example.com",
    );

    expect(typeof served).toBe("function");
    const response = await (served as ServeFetch)(
      new Request("https://origin.example.com/"),
    );
    expect(response.headers.get(EDGE_HEADER)).toBe("cloudflare");
  });
});
