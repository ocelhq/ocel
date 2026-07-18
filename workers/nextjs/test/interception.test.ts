import { describe, expect, it } from "vitest";

import {
  intercept,
  readInterceptionConfig,
  type InterceptDeps,
  type InterceptionConfig,
  type InterceptTarget,
} from "../src/interception";

const cfg: InterceptionConfig = {
  accessKeyId: "AKIA",
  secretKey: "secret",
  region: "us-east-1",
  bucket: "assets",
  table: "state",
  prefix: "prod/proj/app/build",
  tagNamespace: "TAG#prod#proj#app#build#",
};

// A fake AWS signer: routes by host, serving one canned S3 entry and canned DDB
// tag records, and recording the calls so a test can assert what interception
// read. Keys are the S3 object keys (without the .cache.json suffix stripped).
function fakeAws(opts: {
  entries?: Record<string, unknown>;
  tags?: Record<string, { expired?: number; stale?: number }>;
  s3Status?: number;
  ddbStatus?: number;
}): InterceptDeps & { s3Calls: string[]; ddbCalls: unknown[] } {
  const s3Calls: string[] = [];
  const ddbCalls: unknown[] = [];
  const signedFetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = typeof input === "string" ? input : input.toString();
    if (url.includes(".s3.")) {
      const key = decodeURIComponent(new URL(url).pathname.slice(1));
      s3Calls.push(key);
      if (opts.s3Status && opts.s3Status !== 200) {
        return new Response("", { status: opts.s3Status });
      }
      const entry = opts.entries?.[key];
      if (!entry) return new Response("", { status: 404 });
      return new Response(JSON.stringify(entry), { status: 200 });
    }
    // DynamoDB BatchGetItem.
    const body = JSON.parse(String(init?.body));
    ddbCalls.push(body);
    if (opts.ddbStatus && opts.ddbStatus !== 200) {
      return new Response("", { status: opts.ddbStatus });
    }
    const keys = body.RequestItems[cfg.table].Keys as { pk: { S: string } }[];
    const items = [];
    for (const k of keys) {
      const tag = k.pk.S.slice(cfg.tagNamespace.length);
      const rec = opts.tags?.[tag];
      if (!rec) continue;
      const item: Record<string, unknown> = { pk: { S: k.pk.S }, sk: { S: "#META" } };
      if (rec.expired !== undefined) item.expired = { N: String(rec.expired) };
      if (rec.stale !== undefined) item.stale = { N: String(rec.stale) };
      items.push(item);
    }
    return new Response(JSON.stringify({ Responses: { [cfg.table]: items } }), {
      status: 200,
    });
  }) as typeof fetch;
  return { signedFetch, s3Calls, ddbCalls };
}

const s3Key = (routePath: string) =>
  `${cfg.prefix}/cache/${routePath === "/" ? "index" : routePath.replace(/^\//, "")}.cache.json`;

function appPage(opts: { tags?: string; lastModified?: number; postponed?: unknown } = {}) {
  return {
    lastModified: opts.lastModified ?? 1_000,
    value: {
      kind: "APP_PAGE",
      html: "<html>hi</html>",
      rscData: btoa("RSC-PAYLOAD"),
      status: 200,
      headers: opts.tags ? { "x-next-cache-tags": opts.tags } : {},
      ...(opts.postponed !== undefined ? { postponed: opts.postponed } : {}),
    },
  };
}

const req = (init?: RequestInit) => new Request("https://app.example/blog", init);
const target = (over: Partial<InterceptTarget> = {}): InterceptTarget => ({
  routePath: "/blog",
  revalidate: 60,
  ...over,
});

describe("readInterceptionConfig", () => {
  it("is null unless every binding is present", () => {
    expect(readInterceptionConfig({})).toBeNull();
    expect(
      readInterceptionConfig({
        OCEL_EDGE_ACCESS_KEY_ID: "a",
        OCEL_EDGE_SECRET_KEY: "s",
        OCEL_AWS_REGION: "us-east-1",
        OCEL_ISR_BUCKET: "b",
        OCEL_STATE_TABLE: "t",
        OCEL_ISR_PREFIX: "p",
        // missing tag namespace
      }),
    ).toBeNull();
  });

  it("builds a config when all bindings are present", () => {
    const c = readInterceptionConfig({
      OCEL_EDGE_ACCESS_KEY_ID: "a",
      OCEL_EDGE_SECRET_KEY: "s",
      OCEL_AWS_REGION: "us-east-1",
      OCEL_ISR_BUCKET: "b",
      OCEL_STATE_TABLE: "t",
      OCEL_ISR_PREFIX: "p",
      OCEL_ISR_TAG_NAMESPACE: "TAG#p#",
    });
    expect(c).toMatchObject({ accessKeyId: "a", bucket: "b", tagNamespace: "TAG#p#" });
  });
});

describe("intercept", () => {
  it("serves html for a fresh untagged page and never reads DynamoDB", async () => {
    const aws = fakeAws({ entries: { [s3Key("/blog")]: appPage() } });
    const res = await intercept(req(), target(), cfg, { ...aws, now: () => 2_000 });

    expect(res).not.toBeNull();
    expect(res!.status).toBe(200);
    expect(res!.headers.get("content-type")).toBe("text/html; charset=utf-8");
    expect(await res!.text()).toBe("<html>hi</html>");
    // Entry is 1s old (lastModified 1_000, now 2_000), so the CDN gets the
    // remaining window, not the full 60s.
    expect(res!.headers.get("cache-control")).toBe("s-maxage=59");
    // Marks the serve as an interception hit, not a Lambda-origin fill.
    expect(res!.headers.get("x-ocel-isr")).toBe("HIT");
    expect(aws.ddbCalls.length).toBe(0);
  });

  it("serves the RSC payload when the request negotiates RSC", async () => {
    const aws = fakeAws({ entries: { [s3Key("/blog")]: appPage() } });
    const res = await intercept(
      req({ headers: { RSC: "1" } }),
      target(),
      cfg,
      { ...aws, now: () => 2_000 },
    );

    expect(res!.headers.get("content-type")).toBe("text/x-component");
    expect(await res!.text()).toBe("RSC-PAYLOAD");
  });

  it("fails open (null) on an S3 miss", async () => {
    const aws = fakeAws({ entries: {} });
    expect(await intercept(req(), target(), cfg, { ...aws, now: () => 2_000 })).toBeNull();
  });

  it("fails open past the revalidate window (time-based expiry)", async () => {
    const aws = fakeAws({ entries: { [s3Key("/blog")]: appPage({ lastModified: 1_000 }) } });
    // 61s later, revalidate is 60s.
    const res = await intercept(req(), target({ revalidate: 60 }), cfg, {
      ...aws,
      now: () => 1_000 + 61_000,
    });
    expect(res).toBeNull();
  });

  it("stays fresh within the window with a false (static) revalidate", async () => {
    const aws = fakeAws({ entries: { [s3Key("/blog")]: appPage({ lastModified: 1_000 }) } });
    const res = await intercept(req(), target({ revalidate: false }), cfg, {
      ...aws,
      now: () => 1_000 + 10 * 365 * 86400_000,
    });
    expect(res).not.toBeNull();
    expect(res!.headers.get("cache-control")).toBe("s-maxage=31536000");
  });

  it("consults DynamoDB and fails open when a tag was revalidated after the entry", async () => {
    const aws = fakeAws({
      entries: { [s3Key("/blog")]: appPage({ tags: "products", lastModified: 1_000 }) },
      tags: { products: { expired: 1_500 } },
    });
    const res = await intercept(req(), target(), cfg, { ...aws, now: () => 2_000 });
    expect(res).toBeNull();
    expect(aws.ddbCalls.length).toBe(1);
  });

  it("serves a tagged page whose tag expired before the entry was written", async () => {
    const aws = fakeAws({
      entries: { [s3Key("/blog")]: appPage({ tags: "products", lastModified: 1_000 }) },
      tags: { products: { expired: 500 } },
    });
    const res = await intercept(req(), target(), cfg, { ...aws, now: () => 2_000 });
    expect(res).not.toBeNull();
  });

  it("fails open when DynamoDB errors", async () => {
    const aws = fakeAws({
      entries: { [s3Key("/blog")]: appPage({ tags: "products", lastModified: 1_000 }) },
      tags: {},
      ddbStatus: 500,
    });
    const res = await intercept(req(), target(), cfg, { ...aws, now: () => 2_000 });
    expect(res).toBeNull();
  });

  it("forwards (fails open) a partially-postponed PPR page", async () => {
    const aws = fakeAws({
      entries: { [s3Key("/blog")]: appPage({ postponed: "STATE" }) },
    });
    expect(await intercept(req(), target(), cfg, { ...aws, now: () => 2_000 })).toBeNull();
  });

  it("serves an APP_ROUTE body with its stored headers verbatim", async () => {
    const entry = {
      lastModified: 1_000,
      value: {
        kind: "APP_ROUTE",
        body: btoa("{\"ok\":true}"),
        status: 201,
        headers: { "content-type": "application/json" },
      },
    };
    const aws = fakeAws({ entries: { [s3Key("/blog")]: entry } });
    const res = await intercept(req(), target(), cfg, { ...aws, now: () => 2_000 });
    expect(res!.status).toBe(201);
    expect(res!.headers.get("content-type")).toBe("application/json");
    expect(await res!.text()).toBe('{"ok":true}');
  });
});
