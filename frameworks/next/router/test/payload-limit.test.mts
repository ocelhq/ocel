import { describe, expect, it } from "vitest";

import { dispatchResult, type RouteDeps } from "../src/index.mjs";
import { originBodyBudget, type OriginBodyBudget } from "../src/origin-body.mjs";
import { noAssets } from "../test-support/dispatch-scenario.mjs";

const LIMIT = 6_289_408;

function originDeps(overrides: Partial<RouteDeps> = {}): RouteDeps {
  return {
    manifest: {
      entry: "",
      buildId: "test",
      basePath: "",
      pathnames: [],
      routes: {},
      dispatch: { "/api/upload": { kind: "lambda", id: "/api/upload" } },
    },
    functionUrls: { "/api/upload": "https://fn.example.com" },
    slug: "p1",
    deploymentId: "d1",
    app: "web",
    assetStore: noAssets(),
    ...overrides,
  };
}

function recordingDeps(
  budget: OriginBodyBudget | undefined,
): { deps: RouteDeps; calls: () => number; bytes: () => number | undefined } {
  let count = 0;
  let forwarded: number | undefined;
  const deps = originDeps({
    originBodyBudget: budget,
    fetch: (async (req: Request) => {
      count += 1;
      forwarded = (await req.arrayBuffer()).byteLength;
      return new Response("ok", { status: 200 });
    }) as unknown as typeof fetch,
  });
  return { deps, calls: () => count, bytes: () => forwarded };
}

function dispatchUpload(
  deps: RouteDeps,
  body: RequestInit["body"],
  headers: Record<string, string> = {},
): Promise<Response> {
  return dispatchResult(
    {
      resolvedPathname: "/api/upload",
      invocationTarget: { pathname: "/api/upload" },
    },
    new Request("https://app.example/api/upload", {
      method: "POST",
      body,
      headers,
    }),
    deps,
  );
}

describe("originBodyBudget", () => {
  it("is undefined when the origin declares no limit", () => {
    expect(originBodyBudget(undefined)).toBeUndefined();
    expect(originBodyBudget("")).toBeUndefined();
    expect(originBodyBudget("nonsense")).toBeUndefined();
    expect(originBodyBudget("0")).toBeUndefined();
    expect(originBodyBudget("-1")).toBeUndefined();
  });

  it("reads the declared limit", () => {
    expect(originBodyBudget("1024")).toEqual({ maxBytes: 1024 });
  });
});

describe("a declared origin body budget", () => {
  it("passes a body comfortably under the limit through to the origin", async () => {
    const { deps, calls, bytes } = recordingDeps({ maxBytes: LIMIT });

    const body = "x".repeat(1024);
    const res = await dispatchUpload(deps, body, {
      "content-type": "text/plain",
      "content-length": String(body.length),
    });

    expect(calls()).toBe(1);
    expect(bytes()).toBe(1024);
    expect(res.status).toBe(200);
  });

  it("returns 413 without reaching the origin for a text body over the limit", async () => {
    const { deps, calls } = recordingDeps({ maxBytes: LIMIT });

    const size = LIMIT + 1;
    const res = await dispatchUpload(deps, "x".repeat(size), {
      "content-type": "text/plain",
      "content-length": String(size),
    });

    expect(res.status).toBe(413);
    expect(calls()).toBe(0);
  });

  it("measures a binary body raw, so one just under the limit reaches the origin", async () => {
    const { deps, calls } = recordingDeps({ maxBytes: LIMIT });

    const overLimit = LIMIT + 1024;
    const res = await dispatchUpload(deps, new Uint8Array(overLimit), {
      "content-type": "application/octet-stream",
      "content-length": String(overLimit),
    });

    expect(res.status).toBe(413);
    expect(calls()).toBe(0);

    const underLimit = LIMIT - 1024;
    const ok = await dispatchUpload(deps, new Uint8Array(underLimit), {
      "content-type": "application/octet-stream",
      "content-length": String(underLimit),
    });

    expect(ok.status).toBe(200);
    expect(calls()).toBe(1);
  });

  it("takes a five megabyte binary body, the size the probe suite posts", async () => {
    const { deps, calls, bytes } = recordingDeps({ maxBytes: LIMIT });

    const size = 5 * 1024 * 1024;
    const res = await dispatchUpload(deps, new Uint8Array(size), {
      "content-type": "application/octet-stream",
      "content-length": String(size),
    });

    expect(res.status).toBe(200);
    expect(calls()).toBe(1);
    expect(bytes()).toBe(size);
  });

  it("buffers to measure a body that declares no content-length", async () => {
    const { deps, calls } = recordingDeps({ maxBytes: 1024 });

    const chunked = new ReadableStream({
      start(controller) {
        controller.enqueue(new Uint8Array(2048));
        controller.close();
      },
    });
    const res = await dispatchResult(
      {
        resolvedPathname: "/api/upload",
        invocationTarget: { pathname: "/api/upload" },
      },
      new Request("https://app.example/api/upload", {
        method: "POST",
        body: chunked,
        headers: { "content-type": "text/plain" },
        duplex: "half",
      } as RequestInit),
      deps,
    );

    expect(res.status).toBe(413);
    expect(calls()).toBe(0);
  });
});

describe("no declared origin body budget", () => {
  it("forwards a body far over any provider's ceiling untouched", async () => {
    const { deps, calls, bytes } = recordingDeps(undefined);

    const size = LIMIT * 2;
    const res = await dispatchUpload(deps, new Uint8Array(size), {
      "content-type": "application/octet-stream",
      "content-length": String(size),
    });

    expect(res.status).toBe(200);
    expect(calls()).toBe(1);
    expect(bytes()).toBe(size);
  });

  it("forwards a text body far over any provider's ceiling untouched", async () => {
    const { deps, calls } = recordingDeps(undefined);

    const size = LIMIT * 2;
    const res = await dispatchUpload(deps, "x".repeat(size), {
      "content-type": "text/plain",
      "content-length": String(size),
    });

    expect(res.status).toBe(200);
    expect(calls()).toBe(1);
  });
});
