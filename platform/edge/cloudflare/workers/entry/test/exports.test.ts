import { env } from "cloudflare:test";
import { describe, expect, it } from "vitest";
import { newPurgeQueuePort, openPurgeQueue } from "../src/exports";
import type { PurgeQueue } from "../src/purge-queue";

declare module "cloudflare:test" {
  interface ProvidedEnv {
    PURGE_QUEUE: DurableObjectNamespace<PurgeQueue>;
  }
}

describe("the purge queue of a project", () => {
  it("lands each project's raise on that project's own queue, whose tag times answer for no other", async () => {
    const exports = { PurgeQueue: env.PURGE_QUEUE };
    const queue = newPurgeQueuePort(exports);

    await queue.raise(
      { slug: "slug-a", pagesPrefix: "pages/slug-a/prod/web/r00000001" },
      "invalidate",
      [{ tag: "r1|a", raisedAt: 5_000 }],
    );
    await queue.raise(
      { slug: "slug-b", pagesPrefix: "pages/slug-b/prod/web/r00000002" },
      "invalidate",
      [{ tag: "r2|a", raisedAt: 6_000 }],
    );

    expect(await openPurgeQueue(exports, "slug-a").readTimes(["r1|a", "r2|a"])).toEqual({
      "r1|a": { invalidatedAt: 5_000 },
    });
    expect(await openPurgeQueue(exports, "slug-b").readTimes(["r1|a", "r2|a"])).toEqual({
      "r2|a": { invalidatedAt: 6_000 },
    });
  });
});
