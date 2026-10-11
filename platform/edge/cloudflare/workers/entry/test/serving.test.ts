import type { PurgeMode } from "@platform/edge-contract/purge";
import { describe, expect, it } from "vitest";
import type { PurgeOutcome, RaisedTag } from "../src/purge-backlog";
import type { ServeProps } from "../src/serve-key";
import { newServing, type ServingBase } from "../src/serving";

function base(over: { purge?: () => Promise<PurgeOutcome> } = {}) {
  const pending: Promise<unknown>[] = [];
  const stubs: ServeProps[] = [];
  const purged: { mode: PurgeMode; tags: string[] }[] = [];
  const raised: { mode: PurgeMode; tags: RaisedTag[] }[] = [];
  const serving: ServingBase = {
    serve: ({ props }) => {
      stubs.push(props);
      return {
        fetch: async () => new Response(),
        purge: async (mode, tags) => {
          purged.push({ mode, tags });
          return over.purge ? over.purge() : { ok: true };
        },
      };
    },
    queue: {
      raise: async (mode, tags) => {
        raised.push({ mode, tags });
      },
    },
    waitUntil: (promise) => void pending.push(promise),
    now: () => 9_000,
  };
  return { serving, pending, stubs, purged, raised };
}

const scope = { host: "shop.example.com", app: "web", release: "r0a1b2c3d" };
const groups = [{ mode: "purge" as const, tags: ["r0a1b2c3d|a"] }];

describe("the serving of one release", () => {
  it("purges through a Serve stub keyed by the release it serves", async () => {
    const { serving, stubs, purged } = base();

    await newServing(serving, scope).purge(groups, true);

    expect(stubs).toEqual([{ kind: "origin", ...scope }]);
    expect(purged).toEqual([{ mode: "purge", tags: ["r0a1b2c3d|a"] }]);
  });

  it("returns before an unawaited purge lands, and keeps it alive past the response", async () => {
    let finish: (outcome: PurgeOutcome) => void = () => {};
    const { serving, pending, purged } = base({
      purge: () => new Promise<PurgeOutcome>((resolve) => (finish = resolve)),
    });

    await newServing(serving, scope).purge(groups, false);

    expect(pending).toHaveLength(1);
    finish({ ok: true });
    await Promise.all(pending);
    expect(purged).toHaveLength(1);
  });

  it("waits for an awaited purge to land before it returns", async () => {
    let landed = false;
    const { serving } = base({
      purge: async () => {
        await new Promise((done) => setTimeout(done, 10));
        landed = true;
        return { ok: true };
      },
    });

    await newServing(serving, scope).purge(groups, true);

    expect(landed).toBe(true);
  });

  it("stops waiting for an awaited purge that outlasts its bound, and lets it finish behind the response", async () => {
    const { serving, pending } = base({ purge: () => new Promise<PurgeOutcome>(() => {}) });
    const bound = { ...serving, awaitedPurgeMs: 20 };

    await newServing(bound, scope).purge(groups, true);

    expect(pending).toHaveLength(1);
  });

  it("raises the tags on the purge queue at the time the cache refused them", async () => {
    const { serving, raised } = base({
      purge: async () => ({ ok: false, codes: [1134], message: "limit" }),
    });

    await newServing(serving, scope).purge(groups, true);

    expect(raised).toEqual([{ mode: "purge", tags: [{ tag: "r0a1b2c3d|a", raisedAt: 9_000 }] }]);
  });
});
