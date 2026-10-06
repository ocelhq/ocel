import { expect, test } from "vitest";
import { trimVaryForCloudCdn, withCloudCdnVary } from "../src/next/cloud-cdn.mjs";

test("a Next response's Vary keeps the headers the cache key holds and drops the router state tree", () => {
  expect(
    trimVaryForCloudCdn(
      "rsc, next-router-state-tree, next-router-prefetch, next-router-segment-prefetch",
    ),
  ).toBe("rsc, next-router-prefetch, next-router-segment-prefetch");
  expect(trimVaryForCloudCdn("RSC, Next-Router-State-Tree, Next-URL")).toBe("RSC, Next-URL");
});

test("a Vary naming only the router state tree is removed", () => {
  expect(trimVaryForCloudCdn("next-router-state-tree")).toBeNull();
  expect(trimVaryForCloudCdn(null)).toBeNull();
  const response = withCloudCdnVary(
    new Response("x", { headers: { vary: "next-router-state-tree" } }),
  );
  expect(response.headers.has("vary")).toBe(false);
});

test("a Vary naming the interception url keeps it, because the cache key holds it", () => {
  expect(
    trimVaryForCloudCdn(
      "rsc, next-router-state-tree, next-router-prefetch, next-router-segment-prefetch, next-url",
    ),
  ).toBe("rsc, next-router-prefetch, next-router-segment-prefetch, next-url");
});

test("a Vary naming one header twice names it once", () => {
  expect(trimVaryForCloudCdn("rsc, RSC")).toBe("rsc");
});

test("a response whose Vary needs no change is passed through untouched", () => {
  const varied = new Response("x", { headers: { vary: "accept" } });
  const plain = new Response("x");
  expect(withCloudCdnVary(varied)).toBe(varied);
  expect(withCloudCdnVary(plain)).toBe(plain);
  expect(trimVaryForCloudCdn("Accept")).toBe("Accept");
});

test("the rewritten response keeps its status, body and every other header", async () => {
  const response = withCloudCdnVary(
    new Response("payload", {
      status: 203,
      statusText: "Odd",
      headers: {
        vary: "rsc, next-router-state-tree",
        "x-keep": "1",
        "content-type": "text/x-component",
      },
    }),
  );

  expect(response.status).toBe(203);
  expect(response.statusText).toBe("Odd");
  expect(response.headers.get("vary")).toBe("rsc");
  expect(response.headers.get("x-keep")).toBe("1");
  expect(response.headers.get("content-type")).toBe("text/x-component");
  expect(await response.text()).toBe("payload");
});
