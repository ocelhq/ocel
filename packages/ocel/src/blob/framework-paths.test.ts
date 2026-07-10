import { Hono } from "hono";
import { describe, expect, it, vi } from "vitest";
import { z } from "zod";
import { UploadState } from "../gen/proto/runtime/v1/runtime_pb";

vi.mock("../utils/rpc", () => ({
  rpc: { resource: { declare: vi.fn(() => Promise.resolve({})) } },
  ResourceType: { POSTGRES: 1, BUCKET: 2 },
}));

const { bucket } = await import("./bucket");
import { createRouteHandler as honoRouteHandler, uploader as honoUploader } from "./hono";
import { createRouteHandler as expressRouteHandler } from "./express";
import { decodeMetadata } from "./metadata";
import { uploader } from "./uploader";
import type { RuntimeContext } from "./runtime-context";
import type { RuntimeServiceClient } from "./runtime-client";

function fakeContext() {
  const presignUpload = vi.fn(async (_req: unknown) => ({
    sessionId: "sess-1",
    files: [
      { url: "https://store/put/a", key: "avatars/photo.jpg", name: "photo.jpg" },
    ],
  }));
  const client = {
    presignUpload,
    verifyUploadSignature: vi.fn(),
    getUploadStatus: vi.fn(async () => ({ state: UploadState.PENDING, error: "" })),
  } as unknown as RuntimeServiceClient;
  const ctx: RuntimeContext = { client, bucket: "store-bucket" };
  return { ctx, presignUpload };
}

const avatar = uploader(
  {
    input: z.object({ userId: z.string() }),
    middleware: ({ input }) => ({ userId: input.userId }),
  },
  { accept: ["image/*"], path: { prefix: "avatars/" } },
);
const storage = bucket("storage", { uploaders: { avatar } });

const presignBody = {
  uploader: "avatar",
  input: { userId: "u1" },
  files: [{ name: "photo.jpg", size: 10, mimeType: "image/jpeg" }],
};

describe("hono path", () => {
  it("maps a mounted Hono route through the core and returns the presign Response", async () => {
    const { ctx, presignUpload } = fakeContext();
    const app = new Hono();
    app.on(["GET", "POST"], "/api/upload", honoRouteHandler(storage, { runtime: ctx }));

    const res = await app.request("/api/upload?op=presign", {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify(presignBody),
    });

    expect(res.status).toBe(200);
    expect(((await res.json()) as { sessionId: string }).sessionId).toBe("sess-1");
    const req = presignUpload.mock.calls[0]![0] as { callbackBaseUrl: string };
    expect(req.callbackBaseUrl).toBe("http://localhost/api/upload");
  });

  it("gives middleware the Hono Context as `c`", () => {
    // Compile-time intent: `c` is a Hono Context, so context accessors like
    // c.req.header / c.get are available. Runtime just asserts it builds.
    const up = honoUploader(
      { middleware: ({ c }) => ({ ua: c.req.header("user-agent") }) },
      {},
    );
    expect(up.upload).toBeDefined();
  });

  it("hands the Hono Context to middleware at runtime", async () => {
    const { ctx, presignUpload } = fakeContext();
    // middleware reads a header off the Context - only possible if the route
    // passes `c` (not the raw Request) as its middleware arg.
    const ctxBucket = bucket("ctx-storage", {
      uploaders: {
        avatar: honoUploader(
          { middleware: ({ c }) => ({ user: c.req.header("x-user") }) },
          { path: { prefix: "avatars/" } },
        ),
      },
    });
    const app = new Hono();
    app.on(["GET", "POST"], "/api/upload", honoRouteHandler(ctxBucket, { runtime: ctx }));

    const res = await app.request("/api/upload?op=presign", {
      method: "POST",
      headers: { "content-type": "application/json", "x-user": "alice" },
      body: JSON.stringify({
        uploader: "avatar",
        files: [{ name: "photo.jpg", size: 10, mimeType: "image/jpeg" }],
      }),
    });

    expect(res.status).toBe(200);
    const upstream = presignUpload.mock.calls[0]![0] as { metadata: Uint8Array };
    expect(decodeMetadata(upstream.metadata).metadata).toEqual({ user: "alice" });
  });
});

describe("express path", () => {
  it("adapts Node req/res: reads req.body and writes the core Response back", async () => {
    const { ctx, presignUpload } = fakeContext();
    const handler = expressRouteHandler(storage, { runtime: ctx });

    // What express hands a route after `express.json()`: parsed body, path-only
    // url, a header bag, and a Response with status()/setHeader()/end().
    const req = {
      method: "POST",
      url: "/api/upload?op=presign",
      headers: { host: "app.example.com" },
      body: presignBody,
    };

    let statusCode = 0;
    const headers: Record<string, string> = {};
    let ended: Buffer | undefined;
    const res = {
      status(code: number) {
        statusCode = code;
        return this;
      },
      setHeader(k: string, v: string) {
        headers[k] = v;
      },
      end(chunk: Buffer) {
        ended = chunk;
      },
    };

    await new Promise<void>((resolve, reject) => {
      // biome-ignore lint/suspicious/noExplicitAny: fake express req/res
      handler(req as any, { ...res, end: (c: Buffer) => { res.end(c); resolve(); } } as any, reject as any);
    });

    expect(statusCode).toBe(200);
    expect(headers["content-type"]).toContain("application/json");
    expect(JSON.parse(ended!.toString()).sessionId).toBe("sess-1");
    const upstream = presignUpload.mock.calls[0]![0] as { callbackBaseUrl: string };
    expect(upstream.callbackBaseUrl).toBe("http://app.example.com/api/upload");
  });
});
