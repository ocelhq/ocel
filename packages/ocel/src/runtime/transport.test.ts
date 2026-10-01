import { afterEach, describe, expect, it } from "vitest";
import { getRuntimeAddress } from "./transport.js";

describe("getRuntimeAddress", () => {
  afterEach(() => {
    delete process.env.OCEL_RUNTIME_ADDRESS;
  });

  it("reads the one address every runtime-backed resource shares", () => {
    process.env.OCEL_RUNTIME_ADDRESS = "http://127.0.0.1:41235";

    expect(getRuntimeAddress()).toBe("http://127.0.0.1:41235");
  });

  it("throws when the runtime address is undefined", () => {
    expect(() => getRuntimeAddress()).toThrow("OCEL_RUNTIME_ADDRESS");
  });
});
