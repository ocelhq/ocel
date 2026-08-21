import { expect, it } from "vitest";
import { poll } from "../http";
import type { Check } from "../types";

const volatileHeaders = new Set([
  "age",
  "alt-svc",
  "cf-cache-status",
  "cf-ray",
  "connection",
  "content-encoding",
  "content-length",
  "date",
  "keep-alive",
  "nel",
  "report-to",
  "server-timing",
  "transfer-encoding",
  "x-amzn-remapped-date",
  "x-amzn-requestid",
  "x-amzn-trace-id",
  "x-nextjs-cache",
  "x-ocel-cache",
  "x-vercel-cache",
]);

function stableHeaders(headers: Headers) {
  return Object.fromEntries(
    [...headers.entries()].filter(([name]) => !volatileHeaders.has(name)),
  );
}

export const checkGolden: Check = ({ baseUrl, targetName }) => {
  if (targetName !== "aws") return;

  it("keeps prefetch suppression from changing stale responses", async () => {
    const request = async (accept: string, prefetch: boolean) => {
      const response = await fetch(`${baseUrl()}/golden`, {
        headers: {
          accept,
          cookie: "__prerender_bypass=ocel-golden",
          ...(accept === "text/x-component" ? { RSC: "1" } : {}),
          ...(prefetch ? { purpose: "prefetch" } : {}),
        },
        redirect: "manual",
      });
      return {
        status: response.status,
        body: await response.text(),
        headers: stableHeaders(response.headers),
        freshness: response.headers.get("x-nextjs-cache"),
        tier: response.headers.get("x-ocel-cache"),
      };
    };

    for (const accept of ["text/html", "text/x-component"]) {
      const pair = await poll(
        async () => {
          const withHeader = await request(accept, true);
          const without = await request(accept, false);
          return [withHeader, without].some(
            ({ freshness }) => freshness === "STALE",
          )
            ? { withHeader, without }
            : undefined;
        },
        { timeoutMs: 30_000, intervalMs: 1_000 },
      );
      expect(pair).toBeDefined();
      expect(pair!.withHeader.tier).toBe(pair!.without.tier);
      expect({
        status: pair!.withHeader.status,
        body: pair!.withHeader.body,
        headers: pair!.withHeader.headers,
      }).toEqual({
        status: pair!.without.status,
        body: pair!.without.body,
        headers: pair!.without.headers,
      });
      expect(pair!.withHeader.body).toContain("golden-body:v1");
    }
  }, 75_000);
};
