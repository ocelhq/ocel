import { afterEach, describe, expect, it, vi } from "vitest";

vi.mock("../utils/rpc", () => ({
  rpc: { resource: { declare: vi.fn(() => Promise.resolve({})) } },
  ResourceType: { POSTGRES: 1, BUCKET: 2 },
}));

const { bucket } = await import("./bucket");
const { resolveRuntimeContext } = await import("./runtime-context");

afterEach(() => {
  delete process.env.OCEL_RESOURCE_BUCKET_storage;
});

describe("resolveRuntimeContext", () => {
  it("reads the injected address/bucket and builds the typed client", () => {
    process.env.OCEL_RESOURCE_BUCKET_storage = JSON.stringify({
      address: "http://localhost:7070",
      bucket: "org-project-store",
    });

    const ctx = resolveRuntimeContext(bucket("storage", { uploaders: {} }));

    expect(ctx.bucket).toBe("org-project-store");
    expect(typeof ctx.client.presignUpload).toBe("function");
    expect(typeof ctx.client.verifyUploadSignature).toBe("function");
    expect(typeof ctx.client.getUploadStatus).toBe("function");
  });

  it("throws a clear error when the resource config is missing", () => {
    expect(() =>
      resolveRuntimeContext(bucket("storage", { uploaders: {} })),
    ).toThrow(/OCEL_RESOURCE_BUCKET_storage/);
  });
});
