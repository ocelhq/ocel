import { describe, expect, it } from "vitest";

import {
  chooseWindow,
  distribution,
  outcomeOf,
  sizingTable,
  summarizeBurst,
  summarizeGap,
  type BurstTrial,
  type GapSummary,
  type GapTrial,
  type RaceOutcome,
  type RaceResponse,
} from "../src/race-analysis.ts";

const outcome = (over: Partial<RaceOutcome> = {}): RaceOutcome => ({
  seq: 0,
  claimed: false,
  isolate: "aaaa1111",
  colo: "JNB",
  ...over,
});

const gap = (over: { a?: Partial<RaceOutcome>; b?: Partial<RaceOutcome>; deltaMs?: number } = {}): GapTrial => ({
  deltaMs: over.deltaMs ?? 0,
  achievedDeltaMs: over.deltaMs ?? 0,
  a: outcome({ seq: 0, claimed: true, isolate: "aaaa1111", ...over.a }),
  b: outcome({ seq: 1, claimed: false, isolate: "bbbb2222", ...over.b }),
});

const response = (over: Partial<RaceResponse> = {}): RaceResponse => ({
  isolate: "aaaa1111",
  colo: "JNB",
  host: "probe.example.com",
  claimed: true,
  key: "race-1",
  scope: "offzone",
  seq: "0",
  ...over,
});

describe("outcomeOf", () => {
  it("narrows a response that answered the racer that sent it", () => {
    expect(outcomeOf(response(), "race-1", 0)).toEqual({
      seq: 0,
      claimed: true,
      isolate: "aaaa1111",
      colo: "JNB",
    });
  });

  it("refuses a body answered for another racer's seq", () => {
    expect(() => outcomeOf(response({ seq: "1" }), "race-1", 0)).toThrow(
      "received another's body",
    );
  });

  it("refuses a body answered for another key", () => {
    expect(() => outcomeOf(response({ key: "race-2" }), "race-1", 0)).toThrow(
      "received another's body",
    );
  });

  it("refuses a colo-less response rather than defaulting it to a shared unknown", () => {
    // Two racers defaulted to the same "unknown" would compare equal and pass
    // the mixed-colo gate that exists to catch exactly that.
    expect(() => outcomeOf(response({ colo: null }), "race-1", 0)).toThrow("without a colo");
  });
});

describe("distribution", () => {
  it("is null for no samples, because a distribution over nothing is not zero", () => {
    expect(distribution([])).toBeNull();
  });

  it("reports nearest-rank percentiles over the sample itself", () => {
    const d = distribution([10, 1, 5, 2, 9, 3, 4, 8, 7, 6])!;
    expect(d).toMatchObject({ count: 10, min: 1, max: 10, p10: 1, median: 5, p90: 9 });
    expect(d.mean).toBe(5.5);
  });

  it("keeps outliers rather than trimming them", () => {
    const d = distribution([1, 1, 1, 1_000])!;
    expect(d.max).toBe(1_000);
    expect(d.mean).toBe(250.75);
  });
});

describe("summarizeGap", () => {
  it("rates the follower's claims over decidable trials only", () => {
    const summary = summarizeGap(50, [
      gap({ b: { claimed: true } }),
      gap(),
      gap(),
      gap({ b: { colo: "CPT", claimed: true } }),
      gap({ b: { isolate: "aaaa1111", claimed: true } }),
    ]);

    expect(summary).toMatchObject({
      trials: 5,
      decidable: 3,
      secondClaims: 1,
      secondClaimRate: 1 / 3,
    });
    expect(summary.excluded).toMatchObject({ "mixed-colo": 1, "same-isolate": 1 });
  });

  it("excludes a trial the follower won and the leader lost, which measures nothing about the delay", () => {
    const summary = summarizeGap(0, [gap({ a: { claimed: false }, b: { claimed: true } })]);

    expect(summary.excluded["leader-did-not-claim"]).toBe(1);
    expect(summary).toMatchObject({ decidable: 0, secondClaimRate: null });
  });

  it("buckets a trial nobody claimed apart from one the leader lost", () => {
    // Unreachable against the real claim primitive — whoever matches first on a
    // fresh key must miss — but the two are different failures and the runner
    // aborts on only one of them.
    const summary = summarizeGap(0, [gap({ a: { claimed: false } })]);

    expect(summary.excluded).toMatchObject({
      "zero-claims": 1,
      "leader-did-not-claim": 0,
    });
  });

  it("reports no rate at all when nothing was decidable", () => {
    expect(summarizeGap(0, [gap({ b: { colo: "CPT" } })]).secondClaimRate).toBeNull();
  });

  it("reports the achieved delay over every trial, decidable or not", () => {
    const trials = [gap({ deltaMs: 50 }), gap({ deltaMs: 50 })];
    trials[0]!.achievedDeltaMs = 52;
    trials[1]!.achievedDeltaMs = 71;

    expect(summarizeGap(50, trials).achievedDeltaMs).toMatchObject({ count: 2, max: 71 });
  });

  it("keeps the trials it was asked for apart from the trials that answered", () => {
    // A run whose transport failed silently shrinks its own denominator unless
    // both numbers survive into the summary.
    expect(summarizeGap(0, [gap(), gap()], 5)).toMatchObject({ attempted: 5, trials: 2 });
    expect(summarizeGap(0, [gap(), gap()])).toMatchObject({ attempted: 2, trials: 2 });
  });
});

const sweep = (rates: [number, number][], decidable = 100): GapSummary[] =>
  rates.map(([deltaMs, rate]) => ({
    deltaMs,
    attempted: decidable,
    trials: decidable,
    decidable,
    secondClaims: Math.round(rate * decidable),
    secondClaimRate: rate,
    excluded: {
      "zero-claims": 0,
      "leader-did-not-claim": 0,
      "mixed-colo": 0,
      "same-isolate": 0,
    },
    achievedDeltaMs: null,
  }));

describe("chooseWindow", () => {
  it("takes the smallest delay whose suppression the next delay confirms", () => {
    const result = chooseWindow(
      sweep([
        [0, 0.9],
        [50, 0.5],
        [100, 0.02],
        [200, 0.01],
      ]),
    );

    expect(result).toMatchObject({ verdict: "measured", windowMs: 100 });
  });

  it("does not take a drop the next delay walks back", () => {
    const result = chooseWindow(
      sweep([
        [0, 0.9],
        [50, 0.01],
        [100, 0.4],
        [200, 0.01],
      ]),
    );

    expect(result).toMatchObject({ verdict: "exceeds-sweep", windowMs: null });
  });

  it("reports the window as below its own resolution when the smallest delay already suppresses", () => {
    const result = chooseWindow(
      sweep([
        [0, 0.01],
        [50, 0.0],
        [100, 0.0],
      ]),
    );

    expect(result).toMatchObject({ verdict: "below-resolution", windowMs: 0 });
  });

  it("reports a window wider than the sweep rather than picking its last step", () => {
    const result = chooseWindow(
      sweep([
        [0, 0.9],
        [500, 0.8],
        [1_000, 0.7],
      ]),
    );

    expect(result).toMatchObject({ verdict: "exceeds-sweep", windowMs: null });
  });

  it("refuses to order the sweep when any delay is thin, and names the thin ones", () => {
    const summaries = sweep([
      [0, 0.9],
      [100, 0.01],
      [200, 0.01],
    ]);
    summaries[1]!.decidable = 3;

    expect(chooseWindow(summaries)).toMatchObject({
      verdict: "inconclusive",
      windowMs: null,
      undecidedDeltaMs: [100],
    });
  });

  it("is inconclusive over an empty sweep", () => {
    expect(chooseWindow([]).verdict).toBe("inconclusive");
  });
});

const burst = (outcomes: RaceOutcome[], dispersionMs = 1): BurstTrial => ({
  size: outcomes.length,
  dispersionMs,
  outcomes,
});

describe("summarizeBurst", () => {
  const counted = (escapes: number) =>
    burst([
      ...Array.from({ length: escapes }, (_, i) =>
        outcome({ seq: i, claimed: true, isolate: `iso-${i}` }),
      ),
      outcome({ seq: 99, claimed: false, isolate: "iso-quiet" }),
    ]);

  it("reports escapes over counted trials and keeps the other buckets apart", () => {
    const summary = summarizeBurst(
      8,
      [
        counted(2),
        counted(4),
        burst([
          outcome({ seq: 0, claimed: true }),
          outcome({ seq: 1, claimed: false }),
        ]),
        burst([
          outcome({ seq: 0, claimed: true, isolate: "a" }),
          outcome({ seq: 1, claimed: true, isolate: "b", colo: "CPT" }),
        ]),
      ],
      100,
    );

    expect(summary).toMatchObject({
      trials: 4,
      counted: 2,
      singleIsolateTrials: 1,
      singleIsolateRate: 0.25,
    });
    expect(summary.discarded).toEqual({ "zero-claims": 0, "mixed-colo": 1 });
    expect(summary.escapes).toMatchObject({ count: 2, min: 2, max: 4 });
  });

  it("collapses two claims on one isolate into one escape, as L0 does in production", () => {
    const summary = summarizeBurst(
      4,
      [
        burst([
          outcome({ seq: 0, claimed: true, isolate: "aaaa1111" }),
          outcome({ seq: 1, claimed: true, isolate: "aaaa1111" }),
          outcome({ seq: 2, claimed: true, isolate: "bbbb2222" }),
          outcome({ seq: 3, claimed: false, isolate: "cccc3333" }),
        ]),
      ],
      100,
    );

    expect(summary.escapes).toMatchObject({ count: 1, median: 2 });
    expect(summary.rawClaims).toMatchObject({ median: 3 });
    expect(summary.distinctIsolates).toMatchObject({ median: 3 });
  });

  it("discards a burst nobody claimed apart from one that never left an isolate", () => {
    // The first is unreachable against the real claim primitive; the second is
    // not, and folding them together would read L0's collapse as a broken run.
    const nobody = summarizeBurst(
      2,
      [burst([outcome({ seq: 0, isolate: "a" }), outcome({ seq: 1, isolate: "b" })])],
      100,
    );
    expect(nobody.discarded["zero-claims"]).toBe(1);
    expect(nobody.singleIsolateTrials).toBe(0);
  });

  it("calls escapes a lower bound when the racers spread wider than the window", () => {
    const wide = summarizeBurst(2, [counted(2), counted(2)], 10);
    expect(wide.lowerBound).toBe(false);

    const spread = [counted(2), counted(2)].map((t) => ({ ...t, dispersionMs: 40 }));
    expect(summarizeBurst(2, spread, 10).lowerBound).toBe(true);
  });

  it("calls escapes a lower bound when the window is unknown", () => {
    expect(summarizeBurst(2, [counted(2)], null).lowerBound).toBe(true);
  });

  it("keeps the trials it was asked for apart from the trials that answered", () => {
    expect(summarizeBurst(2, [counted(2)], 10, 7)).toMatchObject({ attempted: 7, trials: 1 });
    expect(summarizeBurst(2, [counted(2)], 10)).toMatchObject({ attempted: 1, trials: 1 });
  });
});

describe("sizingTable", () => {
  const inputs = {
    windowSeconds: 0.2,
    isolatesPerColo: 99,
    colos: 300,
    sentinelTtlSeconds: 5,
  };

  it("grows escapes with the arrival rate and the window", () => {
    const [quiet, busy] = sizingTable(inputs, [1, 100]);

    expect(quiet).toMatchObject({ lambdaPerColo: 1, escapesPerColo: 1.2, cappedByIsolates: false });
    expect(busy).toMatchObject({ escapesPerColo: 21, fanInPerStaleEvent: 6_300 });
    expect(busy!.sustainedRequestsPerSecond).toBe(1_260);
  });

  it("caps escapes at the colo's isolate count, since no isolate escapes twice", () => {
    const [flood] = sizingTable(inputs, [100_000]);

    expect(flood).toMatchObject({ escapesPerColo: 99, cappedByIsolates: true });
  });

  it("collapses to one escape per colo when the window is zero", () => {
    const [row] = sizingTable({ ...inputs, windowSeconds: 0 }, [1_000]);

    expect(row).toMatchObject({ escapesPerColo: 1, fanInPerStaleEvent: 300 });
    expect(row!.sustainedRequestsPerSecond).toBe(60);
  });
});
