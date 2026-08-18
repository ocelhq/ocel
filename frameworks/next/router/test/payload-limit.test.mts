import { describe, expect, it } from "vitest";

import { dispatchResult, type RouteDeps } from "../src/index.mjs";
import {
  originBodyBudget,
  originBodyBytes,
  type OriginBodyBudget,
} from "../src/origin-body.mjs";
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

describe("originBodyBytes", () => {
  it("is the raw byte length when the origin takes bodies verbatim", () => {
    expect(originBodyBytes(9_000, "text/plain", "identity")).toBe(9_000);
    expect(originBodyBytes(9_000, "application/octet-stream", "identity")).toBe(9_000);
    expect(originBodyBytes(9_000, null, "identity")).toBe(9_000);
  });

  it("is the raw byte length for a text content type even when the origin base64-encodes", () => {
    expect(originBodyBytes(9_000, "text/plain", "base64")).toBe(9_000);
    expect(originBodyBytes(9_000, "application/json", "base64")).toBe(9_000);
  });

  it("expands to base64's 4/3 ratio for a non-text content type", () => {
    expect(originBodyBytes(3, "application/octet-stream", "base64")).toBe(4);
    expect(originBodyBytes(3_000_000, "application/octet-stream", "base64")).toBe(4_000_000);
  });

  it("treats an absent content type as non-text", () => {
    expect(originBodyBytes(3, null, "base64")).toBe(4);
  });
});

describe("originBodyBudget", () => {
  it("is undefined when the origin declares no limit", () => {
    expect(originBodyBudget(undefined, undefined)).toBeUndefined();
    expect(originBodyBudget(undefined, "base64")).toBeUndefined();
    expect(originBodyBudget("", "base64")).toBeUndefined();
    expect(originBodyBudget("nonsense", "base64")).toBeUndefined();
    expect(originBodyBudget("0", "base64")).toBeUndefined();
    expect(originBodyBudget("-1", "base64")).toBeUndefined();
  });

  it("defaults to identity encoding when the origin declares a limit but no encoding", () => {
    expect(originBodyBudget("1024", undefined)).toEqual({
      maxBytes: 1024,
      encoding: "identity",
    });
  });

  it("reads the declared base64 encoding", () => {
    expect(originBodyBudget("1024", "base64")).toEqual({
      maxBytes: 1024,
      encoding: "base64",
    });
  });
});

describe("a declared origin body budget", () => {
  it("passes a body comfortably under the limit through to the origin", async () => {
    const { deps, calls, bytes } = recordingDeps({
      maxBytes: LIMIT,
      encoding: "base64",
    });

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
    const { deps, calls } = recordingDeps({ maxBytes: LIMIT, encoding: "base64" });

    const size = LIMIT + 1;
    const res = await dispatchUpload(deps, "x".repeat(size), {
      "content-type": "text/plain",
      "content-length": String(size),
    });

    expect(res.status).toBe(413);
    expect(calls()).toBe(0);
  });

  it("trips at roughly 3/4 of the limit when the origin base64-encodes non-text bodies", async () => {
    const { deps, calls } = recordingDeps({ maxBytes: LIMIT, encoding: "base64" });

    const overBinaryLimit = LIMIT - 1024;
    const res = await dispatchUpload(deps, new Uint8Array(overBinaryLimit), {
      "content-type": "application/octet-stream",
      "content-length": String(overBinaryLimit),
    });

    expect(res.status).toBe(413);
    expect(calls()).toBe(0);

    const underBinaryLimit = Math.floor((LIMIT * 3) / 4) - 1024;
    const ok = await dispatchUpload(deps, new Uint8Array(underBinaryLimit), {
      "content-type": "application/octet-stream",
      "content-length": String(underBinaryLimit),
    });

    expect(ok.status).toBe(200);
    expect(calls()).toBe(1);
  });

  it("takes the same binary body when the origin declares identity encoding", async () => {
    const { deps, calls } = recordingDeps({ maxBytes: LIMIT, encoding: "identity" });

    const size = LIMIT - 1024;
    const res = await dispatchUpload(deps, new Uint8Array(size), {
      "content-type": "application/octet-stream",
      "content-length": String(size),
    });

    expect(res.status).toBe(200);
    expect(calls()).toBe(1);
  });

  it("buffers to measure a body that declares no content-length", async () => {
    const { deps, calls } = recordingDeps({ maxBytes: 1024, encoding: "identity" });

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
