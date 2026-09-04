import { createServer } from "node:http";
import type { AddressInfo } from "node:net";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("../utils/rpc", () => ({
  rpc: { resource: { declare: vi.fn(() => Promise.resolve({})) } },
}));

const { bucket } = await import("./bucket.js");
const { resolveBucketContext } = await import("./bucket-context.js");

beforeEach(() => {
  vi.stubEnv("OCEL_PHASE", "");
});

afterEach(() => {
  vi.unstubAllEnvs();
  delete process.env.OCEL_RESOURCE_BUCKET_storage;
  delete process.env.OCEL_RUNTIME_ADDRESS;
  delete process.env.OCEL_SESSION_TOKEN;
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

  it("presents the session token the runtime handed it", async () => {
    const seen: (string | undefined)[] = [];
    const server = createServer((req, res) => {
      seen.push(req.headers.authorization);
      res.statusCode = 500;
      res.end();
    });
    await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", resolve));
    const { port } = server.address() as AddressInfo;

    process.env.OCEL_RESOURCE_BUCKET_storage = JSON.stringify({
      name: "storage",
      bucket: { bucket: "org-project-store" },
    });
    process.env.OCEL_RUNTIME_ADDRESS = `http://127.0.0.1:${port}`;
    process.env.OCEL_SESSION_TOKEN = "session-token";

    const ctx = resolveBucketContext(bucket("storage", { uploaders: {} }));
    await expect(
      ctx.client.presignUpload({ bucket: "org-project-store", files: [] }),
    ).rejects.toThrow();

    await new Promise<void>((resolve, reject) =>
      server.close((err) => (err ? reject(err) : resolve())),
    );
    expect(seen).toEqual(["Bearer session-token"]);
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
