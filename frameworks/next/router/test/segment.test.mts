import { describe, expect, it } from "vitest";

import {
  asSegmentPayload,
  isSegmentPayload,
  isSegmentPrefetch,
} from "../src/segment.mjs";

describe("the segment-prefetch payload guard", () => {
  const H = (init?: Record<string, string>) => new Headers(init);

  it("reads a segment prefetch only where variantPath mints a segment key", () => {
    expect(
      isSegmentPrefetch(H({ RSC: "1", "next-router-segment-prefetch": "/_tree" })),
    ).toBe(true);
    expect(isSegmentPrefetch(H({ "next-router-segment-prefetch": "/_tree" }))).toBe(
      false,
    );
    expect(isSegmentPrefetch(H({ RSC: "1", "next-router-prefetch": "1" }))).toBe(
      false,
    );
  });

  it("accepts the two shapes the client accepts: a segment payload and a 204 miss", () => {
    expect(
      isSegmentPayload(new Response("seg", { headers: { "x-nextjs-postponed": "2" } })),
    ).toBe(true);
    expect(isSegmentPayload(new Response(null, { status: 204 }))).toBe(true);
  });

  it("rejects the postponed shell the origin returns when it cannot answer a segment", () => {
    expect(
      isSegmentPayload(new Response("shell", { headers: { "x-nextjs-postponed": "1" } })),
    ).toBe(false);
    expect(isSegmentPayload(new Response("rsc"))).toBe(false);
  });

  it("replaces a shell answering a segment prefetch with an empty 204, keeping vary", async () => {
    const shell = new Response("shell", {
      headers: { "x-nextjs-postponed": "1", vary: "rsc", "cache-control": "s-maxage=60" },
    });

    const answer = asSegmentPayload(shell);

    expect(answer.status).toBe(204);
    expect(answer.headers.get("vary")).toBe("rsc");
    expect(answer.headers.has("x-nextjs-postponed")).toBe(false);
    expect(await answer.text()).toBe("");
  });

  it("passes a segment payload and a 204 through untouched", () => {
    const segment = new Response("seg", { headers: { "x-nextjs-postponed": "2" } });
    const miss = new Response(null, { status: 204 });
    expect(asSegmentPayload(segment)).toBe(segment);
    expect(asSegmentPayload(miss)).toBe(miss);
  });

  it("leaves a failed origin response alone so the client still sees the failure", () => {
    const failed = new Response("boom", { status: 500 });
    expect(asSegmentPayload(failed)).toBe(failed);
  });
});
