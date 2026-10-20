import type { PurgeMode } from "@platform/edge-contract/purge";
import { describe, expect, it } from "vitest";
import type { RaisedTag } from "../src/purge-backlog";
import type { ReleaseScope } from "../src/purge-times";
import { newServing, type ServingPorts } from "../src/serving";

function base(over: { raise?: () => Promise<void> } = {}) {
  const pending: Promise<unknown>[] = [];
  const raised: { scope: ReleaseScope; mode: PurgeMode; tags: RaisedTag[] }[] = [];
  const serving: ServingPorts = {
    serve: () => ({
      fetch: async () => new Response(),
      purge: async () => ({ ok: true }),
    }),
    queue: {
      raise: async (scope, mode, tags) => {
        raised.push({ scope, mode, tags });
        await over.raise?.();
      },
    },
    waitUntil: (promise) => void pending.push(promise),
    now: () => 9_000,
  };
  return { serving, pending, raised };
}

const scope = {
  host: "shop.example.com",
  app: "web",
  release: "r0a1b2c3d",
  slug: "p1",
  storageCoordinate: "p1/prod/web/r0a1b2c3d",
};
const groups = [{ mode: "purge" as const, tags: ["r0a1b2c3d|a"] }];

describe("the serving of one release", () => {
  it("raises an in-band purge on the purge queue for its own release, at the time it was announced", async () => {
    const { serving, raised } = base();

    await newServing(serving, scope).purge(groups, true);

    expect(raised).toEqual([
      {
        scope: { slug: "p1", storageCoordinate: "p1/prod/web/r0a1b2c3d" },
        mode: "purge",
        tags: [{ tag: "r0a1b2c3d|a", raisedAt: 9_000 }],
      },
    ]);
  });

  it("returns before an unawaited purge lands, and keeps it alive past the response", async () => {
    let finish: () => void = () => {};
    const { serving, pending, raised } = base({
      raise: () => new Promise<void>((resolve) => (finish = resolve)),
    });

    await newServing(serving, scope).purge(groups, false);

    expect(pending).toHaveLength(1);
    finish();
    await Promise.all(pending);
    expect(raised).toHaveLength(1);
  });

  it("waits for an awaited purge to land before it returns", async () => {
    let landed = false;
    const { serving } = base({
      raise: async () => {
        await new Promise((done) => setTimeout(done, 10));
        landed = true;
      },
    });

    await newServing(serving, scope).purge(groups, true);

    expect(landed).toBe(true);
  });

  it("stops waiting for an awaited purge that outlasts its bound, and lets it finish behind the response", async () => {
    const { serving, pending } = base({ raise: () => new Promise<void>(() => {}) });
    const bound = { ...serving, awaitedPurgeMs: 20 };

    await newServing(bound, scope).purge(groups, true);

    expect(pending).toHaveLength(1);
  });
});
