import { Readable } from "node:stream";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { z } from "zod";
import { UploadState } from "../gen/proto/buckets/v1/buckets_pb.js";

vi.mock("../utils/rpc", () => ({
  rpc: { resource: { declare: vi.fn(() => Promise.resolve({})) } },
  ResourceType: { POSTGRES: 1, BUCKET: 2 },
}));

const { bucket } = await import("./bucket.js");
import { encodeMetadata } from "./metadata.js";
import { createRouteHandler } from "./route.js";
import type { BucketContext } from "./bucket-context.js";
import type { BucketServiceClient } from "./bucket-client.js";
import { uploader } from "./uploader.js";

function fakeContext(overrides: Partial<Record<string, unknown>> = {}) {
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
    ...overrides,
  } as unknown as BucketServiceClient;
  const ctx: BucketContext = { client, bucket: "store-bucket" };
  return { ctx, presignUpload };
}

// A Node IncomingMessage-like object: path-only url, a header bag (not a
// Headers instance), and a readable body stream.
function nodeReq(
  pathWithQuery: string,
  body: unknown,
  { host = "app.example.com", drained = false }: { host?: string; drained?: boolean } = {},
) {
  const stream = Readable.from([Buffer.from(JSON.stringify(body))]) as Readable & {
    url: string;
    method: string;
    headers: Record<string, string>;
    readableEnded: boolean;
    body?: unknown;
  };
  stream.url = pathWithQuery;
  stream.method = pathWithQuery.includes("op=poll") ? "GET" : "POST";
  stream.headers = { host };
  // express.json() has already consumed the stream and left the parsed value
  // on req.body; the raw stream would now yield nothing.
  if (drained) stream.body = body;
  return stream;
}

const captured: unknown[] = [];
const avatar = uploader(
  {
    input: z.object({ userId: z.string() }),
    middleware: ({ req, input }) => {
      captured.push(req);
      return { userId: input.userId };
    },
  },
  { accept: ["image/*"], path: { prefix: "avatars/" }, contentDisposition: "inline" },
);
const storage = bucket("storage", { uploaders: { avatar } });

beforeEach(() => {
  captured.length = 0;
});

describe("core accepts a Node IncomingMessage", () => {
  it("reconstructs the absolute callbackBaseUrl from Host + path-only url", async () => {
    const { ctx, presignUpload } = fakeContext();
    const { POST } = createRouteHandler(storage, { runtime: ctx });

    const res = await POST(
      nodeReq("/api/upload?op=presign", {
        uploader: "avatar",
        input: { userId: "u1" },
        files: [{ name: "photo.jpg", size: 10, mimeType: "image/jpeg" }],
      }) as never,
    );

    expect(res.status).toBe(200);
    const req = presignUpload.mock.calls[0]![0] as { callbackBaseUrl: string };
    expect(req.callbackBaseUrl).toBe("http://app.example.com/api/upload");
  });

  it("reads the JSON body from the raw stream", async () => {
    const { ctx, presignUpload } = fakeContext();
    const { POST } = createRouteHandler(storage, { runtime: ctx });

    const res = await POST(
      nodeReq("/api/upload?op=presign", {
        uploader: "avatar",
        input: { userId: "u1" },
        files: [{ name: "photo.jpg", size: 10, mimeType: "image/jpeg" }],
      }) as never,
    );

    expect(res.status).toBe(200);
    expect(presignUpload).toHaveBeenCalledTimes(1);
  });

  it("falls back to req.body when the stream was already drained (express.json)", async () => {
    const { ctx, presignUpload } = fakeContext();
    const { POST } = createRouteHandler(storage, { runtime: ctx });

    const res = await POST(
      nodeReq(
        "/api/upload?op=presign",
        {
          uploader: "avatar",
          input: { userId: "u1" },
          files: [{ name: "photo.jpg", size: 10, mimeType: "image/jpeg" }],
        },
        { drained: true },
      ) as never,
    );

    expect(res.status).toBe(200);
    expect(presignUpload).toHaveBeenCalledTimes(1);
  });

  it("hands the native request (not a normalized wrapper) to middleware", async () => {
    const { ctx } = fakeContext();
    const { POST } = createRouteHandler(storage, { runtime: ctx });

    const native = nodeReq("/api/upload?op=presign", {
      uploader: "avatar",
      input: { userId: "u1" },
      files: [{ name: "photo.jpg", size: 10, mimeType: "image/jpeg" }],
    });
    await POST(native as never);

    expect(captured[0]).toBe(native);
  });
});
