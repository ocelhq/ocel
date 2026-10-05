import { describe, expect, it } from "vitest";

import {
  latest,
  mergeRecord,
  mergeSnapshot,
  newTagPublisher,
  publishTagSnapshot,
  readableSnapshot,
  type StoredTagSnapshot,
  type TagSnapshot,
  type TagSnapshotStore,
} from "../src/index.mjs";

function snapshotOf(
  deployedAt: number,
  records: Record<string, { stale?: number; expired?: number }>,
): TagSnapshot {
  return { version: 1, deployedAt, generatedAt: deployedAt, records };
}

describe("latest", () => {
  it("only ever moves upward, whichever side has the value", () => {
    expect(latest(900, 100)).toBe(900);
    expect(latest(100, 900)).toBe(900);
    expect(latest(undefined, 100)).toBe(100);
    expect(latest(100, undefined)).toBe(100);
    expect(latest(undefined, undefined)).toBeUndefined();
  });
});

describe("mergeRecord", () => {
  it("never walks back a watermark the reader already knows about", () => {
    expect(mergeRecord({ stale: 900, expired: 900 }, { stale: 100, expired: 100 })).toEqual({
      stale: 900,
      expired: 900,
    });
  });

  it("adopts an incoming record the reader has never seen", () => {
    expect(mergeRecord(undefined, { expired: 700 })).toEqual({
      stale: undefined,
      expired: 700,
    });
  });
});

describe("mergeSnapshot", () => {
  it("keeps both sides' invalidations, whichever order they arrive in", () => {
    const merged = mergeSnapshot(
      snapshotOf(0, { theirs: { expired: 500 } }),
      new Map([["ours", { expired: 700 }]]),
      1,
    );
    expect(merged.records.theirs!.expired).toBe(500);
    expect(merged.records.ours!.expired).toBe(700);
  });

  it("prunes records that cannot apply to any entry in this build", () => {
    const merged = mergeSnapshot(
      snapshotOf(5_000, {
        before: { expired: 4_000 },
        atDeploy: { expired: 5_000 },
        after: { expired: 6_000 },
        staleOnly: { stale: 6_000 },
      }),
      new Map(),
      1,
    );
    expect(Object.keys(merged.records).sort()).toEqual(["after", "staleOnly"]);
  });

  it("prunes nothing from a snapshot that was never anchored to a deploy", () => {
    const merged = mergeSnapshot(snapshotOf(0, { ancient: { expired: 1 } }), new Map(), 1);
    expect(merged.records.ancient!.expired).toBe(1);
  });

  it("starts unanchored when there is no prior snapshot to copy an anchor from", () => {
    expect(mergeSnapshot(null, new Map([["products", { expired: 1 }]]), 7)).toEqual({
      version: 1,
      deployedAt: 0,
      generatedAt: 7,
      records: { products: { stale: undefined, expired: 1 } },
    });
  });
});

describe("readableSnapshot", () => {
  it("accepts a document at the version this format is", () => {
    const snapshot = snapshotOf(5_000, { products: { expired: 6_000 } });
    expect(readableSnapshot(snapshot)).toBe(snapshot);
  });

  it("declines a version it was not written against, and a document with no records", () => {
    expect(readableSnapshot({ ...snapshotOf(1, {}), version: 2 })).toBeNull();
    expect(readableSnapshot({ ...snapshotOf(1, {}), records: undefined })).toBeNull();
    expect(readableSnapshot(null)).toBeNull();
  });

  it("declines anything that is not a document", () => {
    for (const parsed of [undefined, 7, "a snapshot", true, [], [snapshotOf(1, {})]]) {
      expect(readableSnapshot(parsed)).toBeNull();
    }
  });
});

function storeLosing(
  losses: number,
  stored: StoredTagSnapshot | null = {
    snapshot: snapshotOf(1, {}),
    etag: '"v1"',
  },
) {
  const calls = { reads: 0, writes: 0 };
  let written: TagSnapshot | null = null;
  const store: TagSnapshotStore = {
    async read() {
      calls.reads++;
      return stored;
    },
    async write(snapshot) {
      calls.writes++;
      if (calls.writes <= losses) return false;
      written = snapshot;
      return true;
    },
  };
  return { store, calls, written: () => written };
}

describe("publishTagSnapshot", () => {
  it("reads and writes once a round, trying again after losing the version", async () => {
    const { store, calls, written } = storeLosing(2);

    await publishTagSnapshot(store, new Map([["cart", { expired: 5 }]]), 10);

    expect(calls).toEqual({ reads: 3, writes: 3 });
    expect(written()?.records).toEqual({ cart: { stale: undefined, expired: 5 } });
  });

  it("gives up with an error after five rounds lost", async () => {
    const { store, calls } = storeLosing(Number.POSITIVE_INFINITY);

    await expect(
      publishTagSnapshot(store, new Map([["cart", { expired: 5 }]]), 10),
    ).rejects.toThrow(/cart/);
    expect(calls).toEqual({ reads: 5, writes: 5 });
  });

  it("refuses to write a snapshot it read with no version to condition the write on", async () => {
    const { store, calls } = storeLosing(0, { snapshot: snapshotOf(1, {}), etag: null });

    await expect(
      publishTagSnapshot(store, new Map([["cart", { expired: 5 }]]), 10),
    ).rejects.toThrow(/version/);
    expect(calls.writes).toBe(0);
  });

  it("publishes nothing where no snapshot was seeded", async () => {
    const { store, calls } = storeLosing(0, null);

    await publishTagSnapshot(store, new Map([["cart", { expired: 5 }]]), 10);

    expect(calls.writes).toBe(0);
  });
});

describe("newTagPublisher", () => {
  it("merges tags published together into one write", async () => {
    const { store, calls, written } = storeLosing(0);
    const publish = newTagPublisher(store);

    await Promise.all([publish("cart", { expired: 5 }), publish("products", { stale: 6 })]);

    expect(calls.writes).toBe(1);
    expect(Object.keys(written()?.records ?? {})).toEqual(["cart", "products"]);
  });
});
