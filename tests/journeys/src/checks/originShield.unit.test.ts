import { afterEach, describe, expect, it } from "bun:test";
import { readOriginAddress, statusOf } from "./originShield";

const realFetch = globalThis.fetch;

afterEach(() => {
  globalThis.fetch = realFetch;
});

function cloudflareServing(records: Record<string, unknown[]>): string[] {
  const asked: string[] = [];
  globalThis.fetch = (async (input: string | URL | Request) => {
    const url = new URL(String(input));
    asked.push(`${url.pathname}${url.search}`);
    const zone = url.searchParams.get("name");
    if (url.pathname === "/client/v4/zones") {
      return Response.json({
        success: true,
        result: zone === "example.com" ? [{ id: "zone1" }] : [],
      });
    }
    return Response.json({ success: true, result: records[zone ?? ""] ?? [] });
  }) as typeof fetch;
  return asked;
}

describe("the origin a proxied record names", () => {
  it("is read from the proxied record of the hostname, in the zone that serves it", async () => {
    cloudflareServing({
      "web.j.example.com": [
        { type: "TXT", content: "unrelated", proxied: false },
        { type: "A", content: "198.51.100.4", proxied: true },
      ],
    });
    expect(await readOriginAddress("web.j.example.com", "token")).toBe("198.51.100.4");
  });

  it("is refused when the hostname has no proxied record", async () => {
    cloudflareServing({
      "web.j.example.com": [{ type: "A", content: "198.51.100.4", proxied: false }],
    });
    await expect(readOriginAddress("web.j.example.com", "token")).rejects.toThrow(
      /no proxied record/,
    );
  });
});

describe("the status an origin answers", () => {
  it("is read off the status line, and absent when nothing answered over HTTP", () => {
    expect(statusOf("HTTP/1.1 403 Forbidden\r\ncontent-length: 0\r\n\r\n")).toBe(403);
    expect(statusOf("")).toBeUndefined();
  });
});
