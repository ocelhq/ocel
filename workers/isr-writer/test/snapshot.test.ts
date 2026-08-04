import { tagSnapshotKey, type TagSnapshot } from "@ocel/next-cache";
import { env } from "cloudflare:test";
import { describe, expect, it } from "vitest";

import { TagClock } from "../src/snapshot";

// One clock per build, so a test's writes never land in another's document —
// the same discipline the worker's own tests keep for the auth memo.
let nextBuild = 0;
function freshPrefix() {
  return `prod/acme/web/SNAP${nextBuild++}`;
}

function clockFor(prefix: string, bucket: R2Bucket = env.OCEL_CACHE_STORE) {
  return new TagClock(bucket, prefix);
}

// What the deploy's genesis seed leaves behind: an empty document carrying the
// build's own deploy time, which is the only place a deployedAt ever comes from.
async function seedGenesis(prefix: string, deployedAt: number) {
  const genesis: TagSnapshot = { version: 1, deployedAt, generatedAt: deployedAt, records: {} };
  await env.OCEL_CACHE_STORE.put(tagSnapshotKey(prefix), JSON.stringify(genesis));
}

async function stored(prefix: string): Promise<TagSnapshot | null> {
  const object = await env.OCEL_CACHE_STORE.get(tagSnapshotKey(prefix));
  return object === null ? null : ((await object.json()) as TagSnapshot);
}

// R2 caps writes to a single key at one per second and answers the loser with
// 429, which is the whole reason this publisher retries at all.
function throttling(failures: number, bucket = env.OCEL_CACHE_STORE): R2Bucket {
  let left = failures;
  return {
    get: (key: string) => bucket.get(key),
    put: (key: string, body: string, options?: R2PutOptions) => {
      if (left-- > 0) {
        throw Object.assign(new Error("put: Too Many Requests (10047)"), { code: 10047 });
      }
      return bucket.put(key, body, options);
    },
  } as unknown as R2Bucket;
}

describe("raise", () => {
  it("leaves R2 holding the merged document before it answers", async () => {
    const prefix = freshPrefix();
    await seedGenesis(prefix, 1_000);

    expect(await clockFor(prefix).raise(new Map([["products", { expired: 5_000 }]]), 9_000)).toBe(
      "published",
    );
    expect(await stored(prefix)).toEqual({
      version: 1,
      deployedAt: 1_000,
      generatedAt: 9_000,
      records: { products: { expired: 5_000 } },
    });
  });

  // deployedAt is written once, by the deploy, and read from the prior document
  // by everything after it. A publisher that recreated the object from scratch
  // would replace the anchor with a zero and permanently disable pruning for
  // that build.
  it("carries the deploy anchor forward across every publish", async () => {
    const prefix = freshPrefix();
    await seedGenesis(prefix, 1_000);
    const clock = clockFor(prefix);

    await clock.raise(new Map([["a", { expired: 5_000 }]]), 9_000);
    await clock.raise(new Map([["b", { expired: 6_000 }]]), 9_500);

    expect((await stored(prefix))?.deployedAt).toBe(1_000);
  });

  // deployedAt has exactly one writer — the deploy's genesis seed — and no
  // second chance. A document created here would carry a zero anchor, against
  // which no record is ever inert, so that build's replica would accumulate
  // every tag it invalidates for the life of the build. Declining costs the
  // build nothing: the edge reads an absent replica as untrusted and falls open
  // to the origin, which is always correct.
  it("creates no snapshot where the deploy seeded none", async () => {
    const prefix = freshPrefix();

    expect(await clockFor(prefix).raise(new Map([["a", { expired: 5_000 }]]), 9_000)).toBe(
      "absent",
    );
    expect(await stored(prefix)).toBeNull();
  });

  // Absence is a fact about R2 at a moment, not about the build: nothing orders
  // the genesis seed against the first invalidation of a build, so an instance
  // that has once found no snapshot must look again rather than decline for its
  // whole lifetime.
  it("publishes into a genesis seed that lands after it first found none", async () => {
    const prefix = freshPrefix();
    const clock = clockFor(prefix);

    expect(await clock.raise(new Map([["a", { expired: 5_000 }]]), 9_000)).toBe("absent");
    await seedGenesis(prefix, 1_000);

    expect(await clock.raise(new Map([["a", { expired: 5_000 }]]), 9_000)).toBe("published");
    expect(await stored(prefix)).toEqual({
      version: 1,
      deployedAt: 1_000,
      generatedAt: 9_000,
      records: { a: { expired: 5_000 } },
    });
  });

  // The DO is single-threaded, so the merge happens in memory and no write ever
  // has to lose a race to another one — which is the whole reason the three
  // contending compare-and-swap loops collapse into this.
  it("converges when a burst of raises lands on one build", async () => {
    const prefix = freshPrefix();
    await seedGenesis(prefix, 1_000);
    const clock = clockFor(prefix);

    // Staggered so the burst spans several publishes rather than collapsing
    // into one: convergence has to hold across them, not merely within one.
    const outcomes = await Promise.all(
      Array.from({ length: 12 }, async (_, i) => {
        for (let tick = 0; tick < i; tick++) await Promise.resolve();
        return clock.raise(new Map([[`tag${i}`, { expired: 5_000 + i }]]), 9_000 + i);
      }),
    );

    expect(outcomes.every((outcome) => outcome === "published")).toBe(true);
    const snapshot = await stored(prefix);
    expect(Object.keys(snapshot!.records).length).toBe(12);
    for (let i = 0; i < 12; i++) expect(snapshot!.records[`tag${i}`]?.expired).toBe(5_000 + i);
  });

  // Pruning is a removal, so the record set is not monotone: a tag absent from
  // the snapshot may be one that was invalidated and then proved inert. The
  // decision to write is made on the merged document, never on which tags the
  // prior one happens to name.
  it("writes nothing for a record the anchor proves can no longer apply", async () => {
    const prefix = freshPrefix();
    await seedGenesis(prefix, 10_000);
    const clock = clockFor(prefix);
    const inert = new Map([["ancient", { expired: 5_000 }]]);

    expect(await clock.raise(inert, 20_000)).toBe("unchanged");
    expect(await clock.raise(inert, 21_000)).toBe("unchanged");
    expect(await stored(prefix)).toEqual({
      version: 1,
      deployedAt: 10_000,
      generatedAt: 10_000,
      records: {},
    });
  });

  it("writes nothing for a watermark the document already stands behind", async () => {
    const prefix = freshPrefix();
    await seedGenesis(prefix, 1_000);
    const clock = clockFor(prefix);

    expect(await clock.raise(new Map([["a", { expired: 9_000 }]]), 5_000)).toBe("published");
    expect(await clock.raise(new Map([["a", { expired: 8_000 }]]), 6_000)).toBe("unchanged");
    expect((await stored(prefix))?.generatedAt).toBe(5_000);
  });

  it("backs off and retries a write R2 rate-limited", async () => {
    const prefix = freshPrefix();
    await seedGenesis(prefix, 1_000);

    const clock = clockFor(prefix, throttling(2));
    expect(await clock.raise(new Map([["a", { expired: 5_000 }]]), 9_000)).toBe("published");
    expect((await stored(prefix))?.records.a?.expired).toBe(5_000);
  });

  // No silent give-up: the records are the caller's to raise again, and only the
  // caller can decide what a lost invalidation costs it.
  it("reports exhaustion rather than dropping the records", async () => {
    const prefix = freshPrefix();
    await seedGenesis(prefix, 1_000);

    const clock = clockFor(prefix, throttling(Number.POSITIVE_INFINITY));
    expect(await clock.raise(new Map([["a", { expired: 5_000 }]]), 9_000)).toBe("exhausted");
    expect(await stored(prefix)).toEqual({
      version: 1,
      deployedAt: 1_000,
      generatedAt: 1_000,
      records: {},
    });
  });

  it("propagates a failure that is not a rate limit", async () => {
    const prefix = freshPrefix();
    await seedGenesis(prefix, 1_000);
    const failing = {
      get: (key: string) => env.OCEL_CACHE_STORE.get(key),
      put: async () => {
        throw new Error("put: Internal Error (10001)");
      },
    } as unknown as R2Bucket;

    await expect(
      clockFor(prefix, failing).raise(new Map([["a", { expired: 5_000 }]]), 9_000),
    ).rejects.toThrow("Internal Error");
  });

  // Merging into a document this publisher cannot read would write away whatever
  // that format carries, the deploy anchor included.
  it("refuses to replace a snapshot it cannot read", async () => {
    const prefix = freshPrefix();
    await env.OCEL_CACHE_STORE.put(tagSnapshotKey(prefix), "{not a snapshot");

    await expect(clockFor(prefix).raise(new Map([["a", { expired: 5_000 }]]), 9_000)).rejects.toThrow();
    expect(await (await env.OCEL_CACHE_STORE.get(tagSnapshotKey(prefix)))!.text()).toBe(
      "{not a snapshot",
    );
  });
});

// The heartbeat is the only evidence a reader has that a build's publisher is
// alive: the document carries no expiry, so an unchanged object otherwise means
// both "nothing has changed" and "nobody has published in a week".
describe("heartbeat", () => {
  it("advances generatedAt with no tag activity", async () => {
    const prefix = freshPrefix();
    await seedGenesis(prefix, 1_000);
    const clock = clockFor(prefix);
    await clock.raise(new Map([["a", { expired: 5_000 }]]), 9_000);

    expect(await clock.heartbeat(20_000)).toBe("published");
    expect(await stored(prefix)).toEqual({
      version: 1,
      deployedAt: 1_000,
      generatedAt: 20_000,
      records: { a: { expired: 5_000 } },
    });
  });

  it("advances it on a build whose publisher has gone cold", async () => {
    const prefix = freshPrefix();
    await seedGenesis(prefix, 1_000);

    expect(await clockFor(prefix).heartbeat(20_000)).toBe("published");
    expect((await stored(prefix))?.generatedAt).toBe(20_000);
  });

  // A build with no snapshot is one the deploy never seeded or the prune has
  // taken. Conjuring an anchorless document for it would leave a snapshot behind
  // that can never prune, for a build that may no longer exist.
  it("creates no snapshot where the deploy seeded none", async () => {
    const prefix = freshPrefix();

    expect(await clockFor(prefix).heartbeat(20_000)).toBe("absent");
    expect(await stored(prefix)).toBeNull();
  });

  it("reports exhaustion when the write never lands", async () => {
    const prefix = freshPrefix();
    await seedGenesis(prefix, 1_000);

    expect(await clockFor(prefix, throttling(Number.POSITIVE_INFINITY)).heartbeat(20_000)).toBe(
      "exhausted",
    );
  });
});
