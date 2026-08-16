import { afterEach, describe, expect, it, vi } from "vitest";

vi.mock("../utils/rpc", () => ({
  rpc: { resource: { declare: vi.fn(() => Promise.resolve({})) } },
}));

const { bucket } = await import("./bucket.js");
const { resolveBucketContext } = await import("./bucket-context.js");

afterEach(() => {
  delete process.env.OCEL_RESOURCE_BUCKET_storage;
  delete process.env.OCEL_RUNTIME_ADDRESS;
});

describe("resolveBucketContext", () => {
  it("reads the bucket binding and the ambient address, and builds the typed client", () => {
    process.env.OCEL_RESOURCE_BUCKET_storage = JSON.stringify({
      name: "storage",
      bucket: { bucket: "org-project-store" },
    });
    process.env.OCEL_RUNTIME_ADDRESS = "http://localhost:7070";

    const ctx = resolveBucketContext(bucket("storage", { uploaders: {} }));

    expect(ctx.bucket).toBe("org-project-store");
    expect(typeof ctx.client.presignUpload).toBe("function");
    expect(typeof ctx.client.verifyUploadSignature).toBe("function");
    expect(typeof ctx.client.getUploadStatus).toBe("function");
  });

  it("throws a clear error when the resource config is missing", () => {
    expect(() =>
      resolveBucketContext(bucket("storage", { uploaders: {} })),
    ).toThrow(/OCEL_RESOURCE_BUCKET_storage/);
  });

  it("throws naming the ambient address when only it is missing", () => {
    process.env.OCEL_RESOURCE_BUCKET_storage = JSON.stringify({
      name: "storage",
      bucket: { bucket: "org-project-store" },
    });

    expect(() =>
      resolveBucketContext(bucket("storage", { uploaders: {} })),
    ).toThrow(/OCEL_RUNTIME_ADDRESS/);
  });
});
