import { createExecutionContext } from "cloudflare:test";
import { describe, expect, it } from "vitest";
import worker, { type Env } from "../src/index";
import type { ServeProps } from "../src/serve-key";
import { answerEveryRecordWith } from "./origin-deps";
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
