import { describe, expect, it } from "vitest";

import {
  summarizeIsolates,
  summarizeSentinel,
  summarizeTtl,
  type SentinelObservation,
  type TtlObservation,
} from "../src/analysis";

const observation = (
  partial: Partial<SentinelObservation> & Pick<SentinelObservation, "reader">,
): SentinelObservation => ({
  colo: "LHR",
  writer: null,
  hit: false,
  elapsedMs: 0,
  ...partial,
});

describe("summarizeIsolates", () => {
  it("counts distinct isolates per colo", () => {
    const summary = summarizeIsolates([
      { colo: "LHR", isolate: "a" },
      { colo: "LHR", isolate: "a" },
      { colo: "LHR", isolate: "b" },
      { colo: "CDG", isolate: "c" },
    ]);

    expect(summary).toEqual([
      { colo: "CDG", isolates: 1, samples: 1 },
      { colo: "LHR", isolates: 2, samples: 3 },
    ]);
  });

  it("is empty for no samples", () => {
    expect(summarizeIsolates([])).toEqual([]);
  });
});

describe("summarizeSentinel", () => {
  it("reports cross-isolate visibility when another isolate reads the sentinel", () => {
    const summary = summarizeSentinel("w", [
      observation({ reader: "w", writer: "w", hit: true, elapsedMs: 20 }),
      observation({ reader: "r1", writer: "w", hit: true, elapsedMs: 90 }),
      observation({ reader: "r2", writer: "w", hit: true, elapsedMs: 140 }),
    ]);

    expect(summary.verdict).toBe("cross-isolate-visible");
    expect(summary.crossIsolateHits).toBe(2);
    expect(summary.readerIsolates).toBe(3);
    expect(summary.firstCrossIsolateHitMs).toBe(90);
  });

  it("reports isolate-local when other isolates read but only the writer hits", () => {
    const summary = summarizeSentinel("w", [
      observation({ reader: "w", writer: "w", hit: true, elapsedMs: 15 }),
      observation({ reader: "r1", hit: false, elapsedMs: 30 }),
      observation({ reader: "r2", hit: false, elapsedMs: 45 }),
    ]);

    expect(summary.verdict).toBe("isolate-local");
    expect(summary.crossIsolateHits).toBe(0);
    expect(summary.firstCrossIsolateHitMs).toBeNull();
  });

  it("reports never-cached when not even the writer's isolate hits", () => {
    const summary = summarizeSentinel("w", [
      observation({ reader: "w", hit: false, elapsedMs: 10 }),
      observation({ reader: "r1", hit: false, elapsedMs: 20 }),
    ]);

    expect(summary.verdict).toBe("never-cached");
  });

  it("is inconclusive when every read landed on the writer's own isolate", () => {
    const summary = summarizeSentinel("w", [
      observation({ reader: "w", writer: "w", hit: true, elapsedMs: 10 }),
      observation({ reader: "w", writer: "w", hit: true, elapsedMs: 20 }),
    ]);

    expect(summary.verdict).toBe("inconclusive");
    expect(summary.readerIsolates).toBe(1);
  });

  it("is inconclusive with no observations at all", () => {
    expect(summarizeSentinel("w", []).verdict).toBe("inconclusive");
  });

  it("counts only observations from the writer's colo", () => {
    const summary = summarizeSentinel("w", [
      observation({ reader: "r1", writer: "w", hit: true, elapsedMs: 50 }),
      observation({ reader: "r2", colo: "CDG", hit: false, elapsedMs: 60 }),
    ]);

    expect(summary.foreignColoObservations).toBe(1);
    expect(summary.readerIsolates).toBe(1);
    expect(summary.verdict).toBe("cross-isolate-visible");
  });
});

describe("summarizeTtl", () => {
  const poll = (elapsedMs: number, hit: boolean): TtlObservation => ({
    elapsedMs,
    hit,
  });

  it("calls the TTL honored only once a hit is observed at or past it", () => {
    const summary = summarizeTtl(10, [
      poll(9_000, true),
      poll(10_500, true),
      poll(11_500, false),
    ]);

    expect(summary.lastHitMs).toBe(10_500);
    expect(summary.firstMissAfterLastHitMs).toBe(11_500);
    expect(summary.verdict).toBe("honored");
  });

  it("declines to judge when the observed lifetime straddles the requested TTL", () => {
    const summary = summarizeTtl(10, [
      poll(1_000, true),
      poll(6_000, true),
      poll(11_000, false),
    ]);

    expect(summary.lastHitMs).toBe(6_000);
    expect(summary.firstMissAfterLastHitMs).toBe(11_000);
    expect(summary.verdict).toBe("indeterminate");
  });

  it("calls a lifetime shorter than requested evicted-early", () => {
    const summary = summarizeTtl(10, [
      poll(1_000, true),
      poll(2_000, false),
      poll(3_000, false),
    ]);

    expect(summary.lastHitMs).toBe(1_000);
    expect(summary.firstMissAfterLastHitMs).toBe(2_000);
    expect(summary.verdict).toBe("evicted-early");
  });

  it("ignores a transient miss followed by a later hit", () => {
    const summary = summarizeTtl(10, [
      poll(1_000, true),
      poll(2_000, false),
      poll(3_000, true),
      poll(12_000, false),
    ]);

    expect(summary.lastHitMs).toBe(3_000);
    expect(summary.firstMissAfterLastHitMs).toBe(12_000);
    expect(summary.transientMisses).toBe(1);
  });

  it("flags an entry that was still live when polling stopped", () => {
    const summary = summarizeTtl(10, [poll(1_000, true), poll(20_000, true)]);

    expect(summary.firstMissAfterLastHitMs).toBeNull();
    expect(summary.stillLiveAtEndOfWindow).toBe(true);
    expect(summary.verdict).toBe("honored");
  });

  it("stays indeterminate when polling stopped before the requested TTL", () => {
    const summary = summarizeTtl(10, [poll(1_000, true), poll(4_000, true)]);

    expect(summary.stillLiveAtEndOfWindow).toBe(true);
    expect(summary.verdict).toBe("indeterminate");
  });

  it("reports never-cached when nothing ever hit", () => {
    expect(summarizeTtl(10, [poll(500, false)]).verdict).toBe("never-cached");
    expect(summarizeTtl(10, []).verdict).toBe("never-cached");
  });
});
