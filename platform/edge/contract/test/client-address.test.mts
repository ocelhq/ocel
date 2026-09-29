import { describe, expect, it } from "vitest";
import { CLIENT_ADDRESS_HEADER, carryClientAddress } from "../src/client-address.mjs";

describe("carryClientAddress", () => {
  it("carries the address the edge saw the client connect from", () => {
    const headers = new Headers();
    carryClientAddress(headers, "203.0.113.9");
    expect(headers.get(CLIENT_ADDRESS_HEADER)).toBe("203.0.113.9");
  });

  it("drops a carrier the client sent, so a client never names its own address", () => {
    const headers = new Headers({ [CLIENT_ADDRESS_HEADER]: "6.6.6.6" });
    carryClientAddress(headers, null);
    expect(headers.get(CLIENT_ADDRESS_HEADER)).toBeNull();
  });
});
