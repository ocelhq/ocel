import { afterEach, describe, expect, it, vi } from "vitest";
import { BindingType } from "../gen/proto/common/bindings/v1/bindings_pb.js";
import { bindingKey } from "../utils/get-config.js";

vi.mock("../utils/rpc", () => ({
  rpc: { resource: { declare: vi.fn(() => Promise.resolve({})) } },
}));

vi.mock("ioredis", () => {
  throw Object.assign(new Error("Cannot find package 'ioredis' imported from ocel/kv"), {
    code: "ERR_MODULE_NOT_FOUND",
  });
});

afterEach(() => {
  vi.unstubAllEnvs();
});

describe("ocel/kv without ioredis installed", () => {
  it("declares a store, and says to install ioredis when the store's client is opened", async () => {
    const { kv } = await import("./index.js");
    vi.stubEnv("OCEL_PHASE", "");
    vi.stubEnv(
      bindingKey("absent", BindingType.KV),
      JSON.stringify({ name: "kv--absent", kv: { host: "127.0.0.1", port: 6379 } }),
    );
    const store = kv("absent", { entries: { hits: kv.counter("hits") } });

    expect(() => store.client).toThrow(
      "a kv store is reached with ioredis, which is not installed. Install it: npm install ioredis",
    );
    await expect(store.hits.get()).rejects.toThrow(/npm install ioredis/);
  });
});
