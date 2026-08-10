import { tagSnapshotKey, type TagSnapshot } from "@ocel/next-cache";
import { env } from "cloudflare:test";
import { describe, expect, it } from "vitest";

import { TagClock } from "../src/snapshot";

let nextBuild = 0;
function freshPrefix() {
  return `prod/acme/web/SNAP${nextBuild++}`;
}

function clockFor(prefix: string, bucket: R2Bucket = env.OCEL_CACHE_STORE) {
  return new TagClock(bucket, prefix);
}

async function seedGenesis(prefix: string, deployedAt: number) {
  const genesis: TagSnapshot = { version: 1, deployedAt, generatedAt: deployedAt, records: {} };
  await env.OCEL_CACHE_STORE.put(tagSnapshotKey(prefix), JSON.stringify(genesis));
}

async function stored(prefix: string): Promise<TagSnapshot | null> {
  const object = await env.OCEL_CACHE_STORE.get(tagSnapshotKey(prefix));
  return object === null ? null : ((await object.json()) as TagSnapshot);
}

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

  it("carries the deploy anchor forward across every publish", async () => {
    const prefix = freshPrefix();
    await seedGenesis(prefix, 1_000);
    const clock = clockFor(prefix);

    await clock.raise(new Map([["a", { expired: 5_000 }]]), 9_000);
    await clock.raise(new Map([["b", { expired: 6_000 }]]), 9_500);

    expect((await stored(prefix))?.deployedAt).toBe(1_000);
  });

  it("creates no snapshot where the deploy seeded none", async () => {
    const prefix = freshPrefix();

    expect(await clockFor(prefix).raise(new Map([["a", { expired: 5_000 }]]), 9_000)).toBe(
      "absent",
    );
    expect(await stored(prefix)).toBeNull();
  });

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

  it("converges when a burst of raises lands on one build", async () => {
    const prefix = freshPrefix();
    await seedGenesis(prefix, 1_000);
    const clock = clockFor(prefix);

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

  it("refuses to replace a snapshot it cannot read", async () => {
    const prefix = freshPrefix();
    await env.OCEL_CACHE_STORE.put(tagSnapshotKey(prefix), "{not a snapshot");

    await expect(clockFor(prefix).raise(new Map([["a", { expired: 5_000 }]]), 9_000)).rejects.toThrow();
    expect(await (await env.OCEL_CACHE_STORE.get(tagSnapshotKey(prefix)))!.text()).toBe(
      "{not a snapshot",
    );
  });
});

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
