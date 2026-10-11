import { createExecutionContext } from "cloudflare:test";
import { describe, expect, it } from "vitest";
import type { Env } from "../src/env";
import { Serve } from "../src/serve";

type PurgeOptions = { tags?: string[]; purgeEverything?: boolean };
type PurgeResult = { success: boolean; errors: { code: number; message: string }[] };

function serveWithCache(cache: object | undefined): Serve {
  const ctx = Object.assign(createExecutionContext(), { cache });
  return new Serve(ctx, {} as Env);
}

function recordingCache(answer: () => PurgeResult | Promise<PurgeResult>) {
  const calls: { method: string; options: PurgeOptions }[] = [];
  const record = (method: string) => async (options: PurgeOptions) => {
    calls.push({ method, options });
    return answer();
  };
  return { calls, purge: record("purge"), invalidate: record("invalidate") };
}

const done: PurgeResult = { success: true, errors: [] };

describe("Serve clearing what it cached", () => {
  it("purges tags through the cache of its own entrypoint", async () => {
    const cache = recordingCache(() => done);

    const outcome = await serveWithCache(cache).purge("purge", ["r1|a", "r1|b"]);

    expect(outcome).toEqual({ ok: true });
    expect(cache.calls).toEqual([{ method: "purge", options: { tags: ["r1|a", "r1|b"] } }]);
  });

  it("invalidates tags, marking entries stale rather than gone", async () => {
    const cache = recordingCache(() => done);

    const outcome = await serveWithCache(cache).purge("invalidate", ["r1|a"]);

    expect(outcome).toEqual({ ok: true });
    expect(cache.calls).toEqual([{ method: "invalidate", options: { tags: ["r1|a"] } }]);
  });

  it("purges everything it cached", async () => {
    const cache = recordingCache(() => done);

    const outcome = await serveWithCache(cache).purgeEverything();

    expect(outcome).toEqual({ ok: true });
    expect(cache.calls).toEqual([{ method: "purge", options: { purgeEverything: true } }]);
  });

  it("reports the codes of a refusal Cloudflare answers with", async () => {
    const cache = recordingCache(() => ({
      success: false,
      errors: [{ code: 1134, message: "rate limit exceeded" }],
    }));

    const outcome = await serveWithCache(cache).purge("purge", ["r1|a"]);

    expect(outcome).toEqual({ ok: false, codes: [1134], message: "1134: rate limit exceeded" });
  });

  it("reports the code of a refusal Cloudflare throws", async () => {
    const cache = recordingCache(() => {
      throw new Error("purge failed: 1134 too many requests");
    });

    const outcome = await serveWithCache(cache).purge("purge", ["r1|a"]);

    expect(outcome).toEqual({
      ok: false,
      codes: [1134],
      message: "purge failed: 1134 too many requests",
    });
  });

  it("refuses rather than succeed when the worker has no cache to purge", async () => {
    const outcome = await serveWithCache(undefined).purge("purge", ["r1|a"]);

    expect(outcome).toMatchObject({ ok: false, codes: [] });
  });
});
