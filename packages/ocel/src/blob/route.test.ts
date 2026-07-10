import { beforeEach, describe, expect, it, vi } from "vitest";
import { z } from "zod";
import { UploadState } from "../gen/proto/runtime/v1/runtime_pb";

vi.mock("../utils/rpc", () => ({
  rpc: { resource: { declare: vi.fn(() => Promise.resolve({})) } },
  ResourceType: { POSTGRES: 1, BUCKET: 2 },
}));

const { bucket } = await import("./bucket");
import { decodeMetadata, encodeMetadata } from "./metadata";
import { createRouteHandler } from "./route";
import type { RuntimeContext } from "./runtime-context";
import type { RuntimeServiceClient } from "./runtime-client";
import { uploader } from "./uploader";

function makeReq(url: string, body?: unknown) {
  return {
    url,
    headers: { get: () => null },
    json: async () => body,
  };
}

function fakeContext(overrides: Partial<Record<string, unknown>> = {}) {
  const presignUpload = vi.fn(async (_req: unknown) => ({
    sessionId: "sess-1",
    files: [{ url: "https://store/put/a", key: "avatars/photo.jpg", name: "photo.jpg" }],
  }));
  const verifyUploadSignature = vi.fn(async (_req: unknown) => ({
    valid: true,
    metadata: encodeMetadata({ uploader: "avatar", metadata: { userId: "u1" } }),
  }));
  const getUploadStatus = vi.fn(async (_req: unknown) => ({
    state: UploadState.PENDING,
    error: "",
  }));

  const client = {
    presignUpload,
    verifyUploadSignature,
    getUploadStatus,
    ...overrides,
  } as unknown as RuntimeServiceClient;

  const ctx: RuntimeContext = { client, bucket: "store-bucket" };
  return { ctx, presignUpload, verifyUploadSignature, getUploadStatus };
}

const onUploadComplete = vi.fn();

const avatar = uploader(
  {
    input: z.object({ userId: z.string() }),
    middleware: ({ input }) => ({ userId: input.userId }),
  },
  {
    accept: ["image/*"],
    limits: { maxFileSize: 1000, maxFileCount: 2, minFileCount: 1 },
    path: { prefix: "avatars/" },
    contentDisposition: "inline",
    onUploadComplete,
  },
);

const storage = bucket("storage", { uploaders: { avatar } });

const presignUrl = "https://app.example.com/api/upload?op=presign";

beforeEach(() => {
  onUploadComplete.mockClear();
});

describe("op=presign", () => {
  it("builds the exact PresignUpload request the runtime expects", async () => {
    const { ctx, presignUpload } = fakeContext();
    const { POST } = createRouteHandler(storage, { runtime: ctx });

    const res = await POST(
      makeReq(presignUrl, {
        uploader: "avatar",
        input: { userId: "u1" },
        files: [{ name: "photo.jpg", size: 500, mimeType: "image/jpeg" }],
      }),
    );

    expect(res.status).toBe(200);
    expect(presignUpload).toHaveBeenCalledTimes(1);
    const req = presignUpload.mock.calls[0]![0] as any;
    expect(req.bucket).toBe("store-bucket");
    expect(req.contentDisposition).toBe("inline");
    expect(req.callbackBaseUrl).toBe("https://app.example.com/api/upload");
    expect(req.files).toEqual([
      { key: "avatars/photo.jpg", name: "photo.jpg", size: 500n, mimeType: "image/jpeg" },
    ]);
    expect(decodeMetadata(req.metadata)).toEqual({
      uploader: "avatar",
      metadata: { userId: "u1" },
    });

    const out = (await res.json()) as any;
    expect(out).toEqual({
      sessionId: "sess-1",
      files: [{ url: "https://store/put/a", key: "avatars/photo.jpg", name: "photo.jpg" }],
    });
  });

  it("short-circuits (no presign) when middleware throws", async () => {
    const rejecting = bucket("s2", {
      uploaders: {
        avatar: uploader<undefined, unknown>(
          {
            middleware: () => {
              throw new Error("Unauthorized");
            },
          },
          { path: { prefix: "a/" } },
        ),
      },
    });
    const { ctx, presignUpload } = fakeContext();
    const { POST } = createRouteHandler(rejecting, { runtime: ctx });

    const res = await POST(
      makeReq(presignUrl, {
        uploader: "avatar",
        files: [{ name: "x.png", size: 1, mimeType: "image/png" }],
      }),
    );

    expect(res.status).toBe(401);
    expect(presignUpload).not.toHaveBeenCalled();
  });

  it("rejects invalid input before minting anything", async () => {
    const { ctx, presignUpload } = fakeContext();
    const { POST } = createRouteHandler(storage, { runtime: ctx });
    const res = await POST(
      makeReq(presignUrl, {
        uploader: "avatar",
        input: { userId: 123 },
        files: [{ name: "p.jpg", size: 1, mimeType: "image/jpeg" }],
      }),
    );
    expect(res.status).toBe(400);
    expect(presignUpload).not.toHaveBeenCalled();
  });

  it("rejects a disallowed mime type", async () => {
    const { ctx, presignUpload } = fakeContext();
    const { POST } = createRouteHandler(storage, { runtime: ctx });
    const res = await POST(
      makeReq(presignUrl, {
        uploader: "avatar",
        input: { userId: "u1" },
        files: [{ name: "doc.pdf", size: 1, mimeType: "application/pdf" }],
      }),
    );
    expect(res.status).toBe(400);
    expect(presignUpload).not.toHaveBeenCalled();
  });

  it("rejects oversized files and count violations", async () => {
    const { ctx } = fakeContext();
    const { POST } = createRouteHandler(storage, { runtime: ctx });

    const oversized = await POST(
      makeReq(presignUrl, {
        uploader: "avatar",
        input: { userId: "u1" },
        files: [{ name: "p.jpg", size: 5000, mimeType: "image/jpeg" }],
      }),
    );
    expect(oversized.status).toBe(400);

    const tooMany = await POST(
      makeReq(presignUrl, {
        uploader: "avatar",
        input: { userId: "u1" },
        files: [
          { name: "a.jpg", size: 1, mimeType: "image/jpeg" },
          { name: "b.jpg", size: 1, mimeType: "image/jpeg" },
          { name: "c.jpg", size: 1, mimeType: "image/jpeg" },
        ],
      }),
    );
    expect(tooMany.status).toBe(400);

    const none = await POST(
      makeReq(presignUrl, {
        uploader: "avatar",
        input: { userId: "u1" },
        files: [],
      }),
    );
    expect(none.status).toBe(400);
  });

  it("returns 404 for an unknown uploader", async () => {
    const { ctx } = fakeContext();
    const { POST } = createRouteHandler(storage, { runtime: ctx });
    const res = await POST(
      makeReq(presignUrl, {
        uploader: "nope",
        files: [{ name: "p.jpg", size: 1, mimeType: "image/jpeg" }],
      }),
    );
    expect(res.status).toBe(404);
  });

  it("resolves limit functions against metadata", async () => {
    const perUser = bucket("s3", {
      uploaders: {
        avatar: uploader(
          {
            input: z.object({ paid: z.boolean() }),
            middleware: ({ input }) => ({ paid: input.paid }),
          },
          {
            limits: {
              maxFileSize: ({ metadata }) => (metadata.paid ? 10000 : 100),
            },
            path: { prefix: "a/" },
          },
        ),
      },
    });
    const { ctx, presignUpload } = fakeContext();
    const { POST } = createRouteHandler(perUser, { runtime: ctx });

    const free = await POST(
      makeReq(presignUrl, {
        uploader: "avatar",
        input: { paid: false },
        files: [{ name: "p.jpg", size: 500, mimeType: "image/jpeg" }],
      }),
    );
    expect(free.status).toBe(400);
    expect(presignUpload).not.toHaveBeenCalled();

    const paid = await POST(
      makeReq(presignUrl, {
        uploader: "avatar",
        input: { paid: true },
        files: [{ name: "p.jpg", size: 500, mimeType: "image/jpeg" }],
      }),
    );
    expect(paid.status).toBe(200);
    expect(presignUpload).toHaveBeenCalledTimes(1);
  });
});

describe("op=callback", () => {
  const callbackUrl = "https://app.example.com/api/upload?op=callback";
  const file = { key: "avatars/photo.jpg", name: "photo.jpg", size: 500, mimeType: "image/jpeg" };

  it("runs onUploadComplete only when the signature is valid", async () => {
    const { ctx, verifyUploadSignature } = fakeContext();
    const { POST } = createRouteHandler(storage, { runtime: ctx });

    const res = await POST(
      makeReq(callbackUrl, { sessionId: "sess-1", signature: "sig", file }),
    );

    expect(res.status).toBe(200);
    expect(verifyUploadSignature).toHaveBeenCalledWith({
      sessionId: "sess-1",
      signature: "sig",
      file: { key: "avatars/photo.jpg", name: "photo.jpg", size: 500n, mimeType: "image/jpeg" },
    });
    expect(onUploadComplete).toHaveBeenCalledWith({
      metadata: { userId: "u1" },
      file: { ...file, path: "avatars/photo.jpg" },
    });
  });

  it("rejects with 401 and skips onUploadComplete on an invalid signature", async () => {
    const { ctx } = fakeContext({
      verifyUploadSignature: vi.fn(async () => ({ valid: false, metadata: new Uint8Array() })),
    });
    const { POST } = createRouteHandler(storage, { runtime: ctx });

    const res = await POST(
      makeReq(callbackUrl, { sessionId: "sess-1", signature: "bad", file }),
    );

    expect(res.status).toBe(401);
    expect(onUploadComplete).not.toHaveBeenCalled();
  });
});

describe("op=poll", () => {
  const pollUrl = "https://app.example.com/api/upload?op=poll&sessionId=sess-1";

  it.each([
    [UploadState.PENDING, "pending"],
    [UploadState.SUCCEEDED, "succeeded"],
    [UploadState.EXPIRED, "expired"],
  ] as const)("maps runtime state %s to %s", async (state, expected) => {
    const { ctx } = fakeContext({
      getUploadStatus: vi.fn(async () => ({ state, error: "" })),
    });
    const { GET } = createRouteHandler(storage, { runtime: ctx });
    const res = await GET(makeReq(pollUrl));
    expect(res.status).toBe(200);
    expect(((await res.json()) as any).state).toBe(expected);
  });

  it("propagates an error message when present", async () => {
    const { ctx } = fakeContext({
      getUploadStatus: vi.fn(async () => ({ state: UploadState.EXPIRED, error: "gone" })),
    });
    const { GET } = createRouteHandler(storage, { runtime: ctx });
    const res = await GET(makeReq(pollUrl));
    expect(((await res.json()) as any).error).toBe("gone");
  });
});
