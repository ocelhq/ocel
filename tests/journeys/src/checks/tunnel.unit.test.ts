import { describe, expect, it } from "bun:test";
import {
  assertNotServed,
  assertRecordKeptAcrossAPromote,
  assertRedirectedOffPlainHTTP,
  assertTunnelAddress,
} from "./tunnel";

describe("the origin a tunneled hostname's proxied record names", () => {
  it("passes when it is a Cloudflare Tunnel", () => {
    expect(() =>
      assertTunnelAddress(
        "0f1e2d3c-4b5a-6978-8a9b-0c1d2e3f4a5b.cfargotunnel.com",
        "web.j.example.com",
      ),
    ).not.toThrow();
  });

  it("fails when it is the box's own address", () => {
    expect(() => assertTunnelAddress("198.51.100.4", "web.j.example.com")).toThrow(
      /198\.51\.100\.4/,
    );
  });
});

describe("a tunneled hostname asked of the box directly", () => {
  it("passes when the box refuses it or answers something other than success", () => {
    expect(() =>
      assertNotServed({ kind: "refused", reason: "handshake failure" }, "the box"),
    ).not.toThrow();
    expect(() =>
      assertNotServed({ kind: "unreachable", reason: "ECONNREFUSED" }, "the box"),
    ).not.toThrow();
    expect(() =>
      assertNotServed(
        {
          kind: "answered",
          status: 421,
          location: undefined,
          said: "HTTP/1.1 421 Misdirected Request",
        },
        "the box",
      ),
    ).not.toThrow();
  });

  it("fails when the box serves it", () => {
    expect(() =>
      assertNotServed(
        {
          kind: "answered",
          status: 200,
          location: undefined,
          said: "HTTP/1.1 200 OK\r\n\r\njourney-hello",
        },
        "the box",
      ),
    ).toThrow(/answered 200/);
  });

  it("fails when the box connected and then said nothing that settles it", () => {
    expect(() =>
      assertNotServed({ kind: "undecided", reason: "closed with nothing said" }, "the box"),
    ).toThrow(/closed with nothing said/);
  });

  it("fails when the box answered with no status line", () => {
    expect(() =>
      assertNotServed(
        { kind: "answered", status: undefined, location: undefined, said: "garbage" },
        "the box",
      ),
    ).toThrow(/garbage/);
  });
});

describe("a tunneled hostname asked over plain http through Cloudflare", () => {
  it("passes on a redirect to the same hostname over https", () => {
    expect(() =>
      assertRedirectedOffPlainHTTP(308, "https://web.j.example.com/", "web.j.example.com"),
    ).not.toThrow();
    expect(() =>
      assertRedirectedOffPlainHTTP(301, "https://web.j.example.com/", "web.j.example.com"),
    ).not.toThrow();
  });

  it("fails when it is served over plain http", () => {
    expect(() => assertRedirectedOffPlainHTTP(200, null, "web.j.example.com")).toThrow(/200/);
  });
});

describe("a tunneled hostname's proxied record across a promote", () => {
  const tunnel = "0f1e2d3c-4b5a-6978-8a9b-0c1d2e3f4a5b.cfargotunnel.com";

  it("passes when it still names the tunnel it named before", () => {
    expect(() =>
      assertRecordKeptAcrossAPromote(tunnel, tunnel, "web.j.example.com", "redeploy"),
    ).not.toThrow();
  });

  it("fails when the promote changed it", () => {
    expect(() =>
      assertRecordKeptAcrossAPromote(
        tunnel,
        "9a8b7c6d-5e4f-4a3b-2c1d-0e9f8a7b6c5d.cfargotunnel.com",
        "web.j.example.com",
        "rollback",
      ),
    ).toThrow(/rollback/);
  });
});
