import { describe, expect, it } from "vitest";

import { resolveServe, type ResolveBase, type ServeFetch } from "../src/index";
import type { DeploymentRecord, DeploymentsBinding } from "../src/deployments";
import { edgeOriginFetch } from "../src/signing";

const FN_URL = "https://abc123.lambda-url.eu-west-2.on.aws/";

function makeRecord(over: Partial<DeploymentRecord> = {}): DeploymentRecord {
  return {
    app: "api",
    framework: "express",
    buildId: "0123456789abcdef",
    routingManifest: null,
    functionUrls: { api: FN_URL },
    assetPrefix: "",
    isrPrefix: "",
    createdAt: 1_000,
    ...over,
  };
}

function bindingReturning(record: DeploymentRecord): DeploymentsBinding {
  return {
    async pointerRecord() {
      return { kind: "record", buildId: record.buildId, record };
    },
  };
}

const assetStore: ResolveBase["assetStore"] = {
  cache: { match: async () => undefined, put: async () => {} },
  waitUntil: () => {},
};

function base(over: Partial<ResolveBase> = {}): ResolveBase {
  return { assetStore, ...over };
}

async function resolved(
  record: DeploymentRecord,
  over: Partial<ResolveBase> = {},
): Promise<ServeFetch | Response> {
  return resolveServe(
    { binding: bindingReturning(record), slug: "p1",
    deploymentId: "d1", host: "api.example.com", app: record.app },
    base(over),
  );
}

function capturing(): { calls: Request[]; fetch: typeof fetch } {
  const calls: Request[] = [];
  return {
    calls,
    fetch: (async (input: RequestInfo | URL, init?: RequestInit) => {
      calls.push(new Request(input as RequestInfo, init));
      return new Response("origin");
    }) as typeof fetch,
  };
}

async function withGlobalFetch<T>(
  replacement: typeof fetch,
  run: () => Promise<T>,
): Promise<T> {
  const original = globalThis.fetch;
  globalThis.fetch = replacement;
  try {
    return await run();
  } finally {
    globalThis.fetch = original;
  }
}

describe("node framework serve path", () => {
  it("forwards to the app's single Function URL, signed with SigV4", async () => {
    const wire = capturing();
    const serve = (await resolved(makeRecord(), {
      originFetch: edgeOriginFetch("AKIAEXAMPLE", "secretkey"),
    })) as ServeFetch;
    expect(serve).toBeTypeOf("function");

    const response = await withGlobalFetch(wire.fetch, () =>
      serve(new Request("https://api.example.com/users?page=2")),
    );

    expect(await response.text()).toBe("origin");
    expect(wire.calls).toHaveLength(1);
    const sent = wire.calls[0];
    expect(sent.url).toBe("https://abc123.lambda-url.eu-west-2.on.aws/users?page=2");
    const auth = sent.headers.get("authorization") ?? "";
    expect(auth).toContain("AWS4-HMAC-SHA256");
    expect(auth).toContain("/eu-west-2/lambda/aws4_request");
    expect(sent.headers.get("x-amz-date")).toBeTruthy();
  });

  it("percent-encodes the second question mark Next writes into an icon URL", async () => {
    const wire = capturing();
    const serve = (await resolved(makeRecord(), { originFetch: wire.fetch })) as ServeFetch;

    await serve(new Request("https://api.example.com/favicon.ico?favicon.abc.ico?dpl=123"));

    expect(wire.calls[0].url).toBe(
      "https://abc123.lambda-url.eu-west-2.on.aws/favicon.ico?favicon.abc.ico%3Fdpl=123",
    );
  });

  it("leaves an ordinary query untouched on its way to the Function URL", async () => {
    const wire = capturing();
    const serve = (await resolved(makeRecord(), { originFetch: wire.fetch })) as ServeFetch;

    await serve(new Request("https://api.example.com/search?a=1&b=2"));

    expect(wire.calls[0].url).toBe(
      "https://abc123.lambda-url.eu-west-2.on.aws/search?a=1&b=2",
    );
  });

  it("sets the forwarded host and proto so the origin can build absolute URLs", async () => {
    const wire = capturing();
    const serve = (await resolved(makeRecord(), { originFetch: wire.fetch })) as ServeFetch;

    await serve(new Request("https://api.example.com/whoami"));

    expect(wire.calls[0].headers.get("x-forwarded-host")).toBe("api.example.com");
    expect(wire.calls[0].headers.get("x-forwarded-proto")).toBe("https");
  });

  it("passes the method, headers and body through to the origin", async () => {
    const wire = capturing();
    const serve = (await resolved(makeRecord(), { originFetch: wire.fetch })) as ServeFetch;

    await serve(
      new Request("https://api.example.com/orders", {
        method: "POST",
        headers: { "content-type": "application/json", cookie: "session=abc" },
        body: JSON.stringify({ sku: "x" }),
      }),
    );

    const sent = wire.calls[0];
    expect(sent.method).toBe("POST");
    expect(sent.headers.get("content-type")).toBe("application/json");
    expect(sent.headers.get("cookie")).toBe("session=abc");
    expect(await sent.json()).toEqual({ sku: "x" });
  });

  it("redirects a protocol-relative path rather than letting it retarget the origin", async () => {
    const wire = capturing();
    const serve = (await resolved(makeRecord(), { originFetch: wire.fetch })) as ServeFetch;

    const response = await serve(new Request("https://api.example.com//evil.com/x?a=1"));

    expect(response.status).toBe(308);
    expect(response.headers.get("location")).toBe("/evil.com/x?a=1");
    expect(wire.calls).toHaveLength(0);
  });

  it("answers 413 for a body over the origin's payload budget", async () => {
    const wire = capturing();
    const serve = (await resolved(makeRecord(), {
      originFetch: wire.fetch,
      originBodyBudget: { maxBytes: 16, encoding: "identity" },
    })) as ServeFetch;

    const response = await serve(
      new Request("https://api.example.com/upload", {
        method: "POST",
        headers: { "content-type": "application/octet-stream" },
        body: new Uint8Array(64),
      }),
    );

    expect(response.status).toBe(413);
    expect(wire.calls).toHaveLength(0);
  });

  it("forwards a body inside the origin's payload budget", async () => {
    const wire = capturing();
    const serve = (await resolved(makeRecord(), {
      originFetch: wire.fetch,
      originBodyBudget: { maxBytes: 1024, encoding: "identity" },
    })) as ServeFetch;

    await serve(
      new Request("https://api.example.com/orders", {
        method: "POST",
        headers: { "content-type": "application/json" },
        body: JSON.stringify({ sku: "x" }),
      }),
    );

    expect(await wire.calls[0].json()).toEqual({ sku: "x" });
  });

  it("binds none of next's machinery onto the forwarded request", async () => {
    const wire = capturing();
    const serve = (await resolved(makeRecord(), { originFetch: wire.fetch })) as ServeFetch;

    const response = await serve(new Request("https://api.example.com/_next/data/x/y.json"));

    const sent = wire.calls[0];
    expect(sent.url).toBe("https://abc123.lambda-url.eu-west-2.on.aws/_next/data/x/y.json");
    expect(sent.headers.get("x-nextjs-data")).toBeNull();
    expect(sent.headers.get("x-ocel-entry")).toBeNull();
    expect(response.headers.get("x-matched-path")).toBeNull();
  });

  it("answers 502 rather than guessing when the app published no Function URL", async () => {
    const wire = capturing();
    const serve = (await resolved(makeRecord({ functionUrls: {} }), {
      originFetch: wire.fetch,
    })) as ServeFetch;

    const response = await serve(new Request("https://api.example.com/"));

    expect(response.status).toBe(502);
    expect(await response.text()).toMatch(/No function URL for api/);
    expect(wire.calls).toHaveLength(0);
  });

  it("answers 502 rather than guessing when the app published several Function URLs", async () => {
    const wire = capturing();
    const serve = (await resolved(
      makeRecord({ functionUrls: { api: FN_URL, worker: "https://d.lambda-url.eu-west-2.on.aws/" } }),
      { originFetch: wire.fetch },
    )) as ServeFetch;

    const response = await serve(new Request("https://api.example.com/"));

    expect(response.status).toBe(502);
    expect(await response.text()).toMatch(/2 function URLs/);
    expect(wire.calls).toHaveLength(0);
  });

  it("answers 502 rather than calling the IAM-guarded origin unsigned", async () => {
    const wire = capturing();
    const serve = (await resolved(makeRecord(), { fetch: wire.fetch })) as ServeFetch;

    const response = await withGlobalFetch(wire.fetch, () =>
      serve(new Request("https://api.example.com/")),
    );

    expect(response.status).toBe(502);
    expect(await response.text()).toMatch(/signing credentials/);
    expect(wire.calls).toHaveLength(0);
  });

  it("serves fastify and hono through the same path", async () => {
    for (const framework of ["fastify", "hono"]) {
      const wire = capturing();
      const serve = (await resolved(makeRecord({ framework }), {
        originFetch: wire.fetch,
      })) as ServeFetch;

      await serve(new Request("https://api.example.com/ping"));

      expect(wire.calls[0].url).toBe("https://abc123.lambda-url.eu-west-2.on.aws/ping");
    }
  });

  it("passes a framework it has never heard of through to the origin", async () => {
    const wire = capturing();
    const serve = (await resolved(makeRecord({ framework: "sveltekit" }), {
      originFetch: wire.fetch,
    })) as ServeFetch;

    await serve(new Request("https://api.example.com/ping"));

    expect(wire.calls[0].url).toBe("https://abc123.lambda-url.eu-west-2.on.aws/ping");
  });

  it("routes a next deployment through next's own serve", async () => {
    const record = makeRecord({
      framework: "next",
      functionUrls: {},
      routingManifest: {
        buildId: "build-1",
        basePath: "",
        pathnames: [],
        routes: {},
        dispatch: {},
      },
    });
    const serve = (await resolved(record)) as ServeFetch;

    const response = await serve(new Request("https://api.example.com//doubled"));

    expect(response.status).toBe(308);
    expect(response.headers.get("location")).toBe("/doubled");
  });
});
