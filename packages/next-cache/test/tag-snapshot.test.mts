import { describe, expect, it } from "vitest";

import {
  latest,
  mergeRecord,
  mergeSnapshot,
  readableSnapshot,
  type TagSnapshot,
} from "../src/index.mjs";

function snapshotOf(
  deployedAt: number,
  records: Record<string, { stale?: number; expired?: number }>,
): TagSnapshot {
  return { version: 1, deployedAt, generatedAt: deployedAt, records };
}

describe("latest", () => {
  it("only ever moves upward, whichever side carries the value", () => {
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
  it("carries both sides' invalidations, whichever order they arrive in", () => {
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

  it("starts unanchored when there is no prior snapshot to carry an anchor from", () => {
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
