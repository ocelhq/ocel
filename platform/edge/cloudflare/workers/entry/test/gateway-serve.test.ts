import { createExecutionContext, env as testEnv } from "cloudflare:test";
import type { NextRouteTable } from "@framework/next-protocol/route-table";
import { describe, expect, it } from "vitest";
import worker, { type Env } from "../src/index";
import {
  buildOriginUrl,
  type RenderCoordinates,
  readRenderCall,
  type ServeProps,
} from "../src/serve-key";
import { answerEveryRecordWith, capturing, withGlobalFetch } from "./origin-deps";
import { routeTableKey } from "./route-table-store";

const record = {
  app: "web",
  framework: "next",
  release: "deploy-1",
  buildId: "deploy-1",
  routeTable: { format: "next" as const, key: routeTableKey() },
  functionUrls: { "/": "https://abc.lambda-url.eu-west-2.on.aws/" },
  assetPrefix: "prod/p1/web/deploy-1/assets",
  isrPrefix: "prod/p1/web/deploy-1/isr",
  createdAt: 1_000,
};

const env: Env = {
  RELEASES: answerEveryRecordWith(async () => ({ kind: "record", release: "deploy-1", record })),
  OCEL_SLUG: "p1",
  OCEL_ASSET_BUCKET: "assets-bucket",
  OCEL_AWS_REGION: "eu-west-2",
  OCEL_EDGE_ACCESS_KEY_ID: "AKIAEXAMPLE",
  OCEL_EDGE_SECRET_KEY: "secretkey",
};

function ctxWithServe(answer: (key: string) => Response) {
  const asked: { key: string; props: ServeProps }[] = [];
  const headers: Headers[] = [];
  const serve = ({ props }: { props: ServeProps }) => ({
    fetch: async (request: Request) => {
      const key = decodeURIComponent(new URL(request.url).pathname.slice(1));
      asked.push({ key, props });
      headers.push(request.headers);
      return answer(key);
    },
  });
  const ctx = createExecutionContext();
  Object.defineProperty(ctx, "exports", { value: { Serve: serve, CacheEntrypoint: () => ({}) } });
  return { asked, headers, ctx };
}

describe("the gateway reading a release's immutable objects", () => {
  it("reads the route table through Serve, keyed by the request's host and the release's coordinates", async () => {
    const { asked, ctx } = ctxWithServe(() => new Response("Not Found", { status: 404 }));

    const response = await worker.fetch(new Request("https://shop.example.com/users"), env, ctx);

    expect(asked).toEqual([
      {
        key: routeTableKey(),
        props: { kind: "object", host: "shop.example.com", app: "web", release: "deploy-1" },
      },
    ]);
    expect(response.status).toBe(503);
  });

  it("keys the same release's route table by each host that asks for it", async () => {
    const hosts: string[] = [];
    for (const host of ["a.example.com", "b.example.com"]) {
      const { asked, ctx } = ctxWithServe(() => new Response("Not Found", { status: 404 }));
      await worker.fetch(new Request(`https://${host}/users`), env, ctx);
      hosts.push(asked[0].props.host);
    }
    expect(hosts).toEqual(["a.example.com", "b.example.com"]);
  });
});

describe("the gateway on an origin whose bucket an HMAC key reads", () => {
  const hmacEnv: Env = {
    RELEASES: env.RELEASES,
    OCEL_SLUG: "p1",
    OCEL_CACHE_STORE: testEnv.TAG_SNAPSHOT_STORE,
    OCEL_ASSET_STORE_ENDPOINT: "https://objects.example.com",
    OCEL_ASSET_STORE_BUCKET: "ocel-acme-production",
    OCEL_ASSET_STORE_PREFIX: "assets/",
    OCEL_ASSET_STORE_ACCESS_KEY_ID: "GOOG1EXAMPLEACCESSID",
    OCEL_ASSET_STORE_SECRET_KEY: "examplesecret",
  };
  const table: NextRouteTable = {
    rootFunction: "/",
    buildId: "deploy-1",
    basePath: "",
    pathnames: [],
    routes: {
      beforeMiddleware: [],
      beforeFiles: [],
      afterFiles: [],
      dynamicRoutes: [],
      onMatch: [],
      fallback: [],
    },
    dispatch: {},
  };

  it("reads the route table and a static file through Serve, and the bucket never itself", async () => {
    const { asked, ctx } = ctxWithServe((key) =>
      key === routeTableKey() ? new Response(JSON.stringify(table)) : new Response("chunk"),
    );
    const wire = capturing();

    const response = await withGlobalFetch(wire.fetch, () =>
      worker.fetch(new Request("https://shop.example.com/_next/static/app.js"), hmacEnv, ctx),
    );

    expect(asked.map(({ key }) => key)).toEqual([
      routeTableKey(),
      "prod/p1/web/deploy-1/assets/_next/static/app.js",
    ]);
    expect(wire.calls).toHaveLength(0);
    expect(await response.text()).toBe("chunk");
  });
});

describe("the gateway calling Serve", () => {
  it("hands Serve none of the visitor's cookies or credentials", async () => {
    const { headers, ctx } = ctxWithServe(() => new Response("Not Found", { status: 404 }));

    await worker.fetch(
      new Request("https://shop.example.com/users", {
        headers: { cookie: "session=1", authorization: "Bearer t" },
      }),
      env,
      ctx,
    );

    expect(headers.length).toBeGreaterThan(0);
    for (const sent of headers) {
      expect(sent.get("cookie")).toBeNull();
      expect(sent.get("authorization")).toBeNull();
    }
  });
});

describe("the gateway serving a prerender", () => {
  const prerenderRecord = {
    ...record,
    release: "deploy-1~f1",
    isrPrefix: "prod/p1/web/r0a1b2c3d/isr",
  };
  const table: NextRouteTable = {
    rootFunction: "/",
    buildId: "deploy-1",
    basePath: "",
    pathnames: ["/blog"],
    routes: {
      beforeMiddleware: [],
      beforeFiles: [],
      afterFiles: [],
      dynamicRoutes: [],
      onMatch: [],
      fallback: [],
    },
    dispatch: { "/blog": { kind: "prerender", id: "/", config: {} } },
  };
  const prerenderEnv: Env = {
    ...env,
    RELEASES: answerEveryRecordWith(async () => ({
      kind: "record",
      release: prerenderRecord.release,
      record: prerenderRecord,
    })),
  };

  function ctxServing() {
    const origin: (RenderCoordinates & { props: ServeProps; originUrl: string })[] = [];
    const serve = ({ props }: { props: ServeProps }) => ({
      fetch: async (request: Request) => {
        if (props.kind === "origin") {
          origin.push({
            props,
            ...readRenderCall(request.url),
            originUrl: buildOriginUrl(request.url),
          });
          return new Response("cached page", {
            headers: { "cf-cache-status": "HIT", "cache-tag": "r0a1b2c3d" },
          });
        }
        const key = decodeURIComponent(new URL(request.url).pathname.slice(1));
        return key === routeTableKey()
          ? new Response(JSON.stringify(table))
          : new Response("", { status: 404 });
      },
      purge: async () => ({ ok: true }),
    });
    const ctx = createExecutionContext();
    Object.defineProperty(ctx, "exports", { value: { Serve: serve, CacheEntrypoint: () => ({}) } });
    return { origin, ctx };
  }

  it("serves the prerender through Serve, keyed by the release token of its ISR prefix", async () => {
    const { origin, ctx } = ctxServing();

    const response = await worker.fetch(
      new Request("https://shop.example.com/blog"),
      prerenderEnv,
      ctx,
    );

    expect(origin).toEqual([
      {
        props: { kind: "origin" },
        host: "shop.example.com",
        app: "web",
        release: "r0a1b2c3d",
        variant: "",
        originUrl: "https://abc.lambda-url.eu-west-2.on.aws/blog",
      },
    ]);
    expect(response.headers.get("x-ocel-cache")).toBe("HIT");
    expect(await response.text()).toBe("cached page");
  });

  it("serves a preview's prerender through Serve, keyed by the preview host and its release token", async () => {
    const { origin, ctx } = ctxServing();
    const wire = capturing();
    const host = "pr-12-web-abcdefghijklmnopp3347l26.myapp.com";

    const response = await withGlobalFetch(wire.fetch, () =>
      worker.fetch(
        new Request(`https://${host}/blog`),
        {
          ...prerenderEnv,
          OCEL_PREVIEW: "1",
          OCEL_PREVIEW_BASE_DOMAIN: "myapp.com",
          OCEL_PREVIEW_KEY: "0f1e2d3c4b5a69788796a5b4c3d2e1f00f1e2d3c4b5a69788796a5b4c3d2e1f0",
        },
        ctx,
      ),
    );

    expect(origin).toEqual([
      {
        props: { kind: "origin" },
        host,
        app: "web",
        release: "r0a1b2c3d",
        variant: "",
        originUrl: "https://abc.lambda-url.eu-west-2.on.aws/blog",
      },
    ]);
    expect(wire.calls).toHaveLength(0);
    expect(response.headers.get("x-ocel-cache")).toBe("HIT");
    expect(await response.text()).toBe("cached page");
  });
});
