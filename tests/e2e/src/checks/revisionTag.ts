import assert from "node:assert/strict";
import { randomUUID } from "node:crypto";
import type { Check, Fetch } from "./context";

const ORIGIN_SECRET_HEADER = "x-ocel-origin-secret";
const REFUSED = 403;
const REFUSED_PATHS = ["/", "/health"];

async function statusOf(fetch: Fetch, url: string, init?: RequestInit): Promise<number> {
  const res = await fetch(url, { ...init, redirect: "manual" });
  await res.body?.cancel();
  return res.status;
}

export const revisionTagChecks: Check[] = [
  {
    title:
      "each revision-tag URL refuses a request with no origin secret or a guessed one, and the edge's hostname answers",
    run: async (ctx) => {
      const tags = await ctx.readRevisionTagUrls();
      assert.ok(
        tags.length > 0,
        `${ctx.app} has no revision-tag URL, so the edge has no release to reach and nothing around it to ask`,
      );
      for (const tag of tags) {
        for (const path of REFUSED_PATHS) {
          const url = `${tag}${path}`;
          const bare = await statusOf(ctx.fetch, url);
          assert.equal(
            bare,
            REFUSED,
            `${url} answered ${bare} to a request with no origin secret; want ${REFUSED} before the app runs`,
          );
          const guessed = await statusOf(ctx.fetch, url, {
            headers: { [ORIGIN_SECRET_HEADER]: randomUUID() },
          });
          assert.equal(
            guessed,
            REFUSED,
            `${url} answered ${guessed} to a request with a guess at the origin secret; want ${REFUSED} before the app runs`,
          );
        }
      }
      const through = await statusOf(ctx.fetch, `${ctx.baseUrl}/health`);
      assert.equal(through, 200, `${ctx.baseUrl}/health answered ${through} through the edge`);
    },
  },
];
