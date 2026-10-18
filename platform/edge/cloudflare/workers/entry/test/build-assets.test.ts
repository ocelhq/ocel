import { createExecutionContext } from "cloudflare:test";
import type { NextRouteTable } from "@framework/next-protocol/route-table";
import { describe, expect, it } from "vitest";
import worker, { type Env } from "../src/index";
import type { StoredRecord } from "../src/releases";
import { answerEveryRecordWith, awsOrigin } from "./origin-deps";
import { routeTableKey } from "./route-table-store";

const table: NextRouteTable = {
  rootFunction: "/",
  buildId: "build-2",
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

const record: StoredRecord = {
  app: "web",
  framework: "next",
  release: "deploy-2",
  buildId: "build-2",
  rootFunction: "/",
  routeTable: { format: "next", key: routeTableKey() },
  static: { immutablePrefixes: ["/_next/static/"] },
  functionUrls: { "/": "https://abc.lambda-url.eu-west-2.on.aws/" },
  assetPrefix: "prod/p1/web/deploy-2/assets",
  isrPrefix: "prod/p1/web/deploy-2/isr",
  createdAt: 1_000,
  edge: {},
  buildAssets: { "build-1": "prod/p1/web/deploy-1/assets" },
};

const env: Env = {
  RELEASES: answerEveryRecordWith(async () => ({ kind: "record", release: "deploy-2", record })),
  OCEL_SLUG: "p1",
  OCEL_ORIGIN: awsOrigin({ bucket: "assets-bucket", region: "eu-west-2" }),
};

async function assetKeyRead(url: string): Promise<string | undefined> {
  const asked: string[] = [];
  const serve = () => ({
    fetch: async (request: Request) => {
      const key = decodeURIComponent(new URL(request.url).pathname.slice(1));
      asked.push(key);
      return key === routeTableKey() ? new Response(JSON.stringify(table)) : new Response("chunk");
    },
  });
  const ctx = createExecutionContext();
  Object.defineProperty(ctx, "exports", { value: { Serve: serve } });
  await worker.fetch(new Request(url), env, ctx);
  return asked.find((key) => key !== routeTableKey());
}

describe("an asset request naming the build that linked it", () => {
  it("reads an immutable asset from the prefix of the earlier build its dpl names", async () => {
    expect(
      await assetKeyRead("https://shop.example.com/_next/static/chunks/a.js?dpl=build-1"),
    ).toBe("prod/p1/web/deploy-1/assets/_next/static/chunks/a.js");
  });

  it("reads from the served release's prefix when dpl names the build it serves", async () => {
    expect(
      await assetKeyRead("https://shop.example.com/_next/static/chunks/a.js?dpl=build-2"),
    ).toBe("prod/p1/web/deploy-2/assets/_next/static/chunks/a.js");
  });

  it("reads from the served release's prefix when dpl names a build the pointer never served", async () => {
    expect(
      await assetKeyRead("https://shop.example.com/_next/static/chunks/a.js?dpl=build-9"),
    ).toBe("prod/p1/web/deploy-2/assets/_next/static/chunks/a.js");
  });

  it("reads a path outside the immutable prefixes from the served release whatever dpl names", async () => {
    expect(await assetKeyRead("https://shop.example.com/robots.txt?dpl=build-1")).toBe(
      "prod/p1/web/deploy-2/assets/robots.txt",
    );
  });
});
