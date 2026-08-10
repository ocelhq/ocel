import { Abort } from "./abort.ts";

export interface RaceOutcome {
  seq: number;
  claimed: boolean;
  isolate: string;
  colo: string;
  delayMs: number;
}

export interface RaceResponse {
  isolate: string;
  colo: string | null;
  host: string;
  claimed: boolean;
  key: string;
  scope: string;
  seq: string | null;
  delayMs: number;
}

export interface RacerTiming {
  jitterMs: number;
  elapsedMs: number;
}

export function outcomeOf(
  response: RaceResponse,
  key: string,
  seq: number,
  timing: RacerTiming,
): RaceOutcome {
  if (response.key !== key || response.seq !== String(seq)) {
    throw new Abort(
      `racer ${seq} on key ${key} was answered for key ${response.key} seq ` +
        `${response.seq}. One racer received another's body; the run is void.`,
    );
  }
  if (response.colo === null) {
    throw new Abort(
      `racer ${seq} on key ${key} was served without a colo. request.cf was absent, so ` +
        "no trial can be attributed to a colo and the run is void.",
    );
  }
  if (
    !Number.isFinite(response.delayMs) ||
    response.delayMs < 0 ||
    response.delayMs > timing.jitterMs
  ) {
    throw new Abort(
      `racer ${seq} on key ${key} reported an admission delay of ${response.delayMs}ms, ` +
        `outside [0, ${timing.jitterMs}]. The worker is not drawing the window the run ` +
        "asked for; the run is void.",
    );
  }
  if (response.delayMs > timing.elapsedMs) {
    throw new Abort(
      `racer ${seq} on key ${key} reported sleeping ${response.delayMs}ms inside a request ` +
        `the driver measured at ${timing.elapsedMs.toFixed(1)}ms. The delay was reported ` +
        "but not spent; the run is void.",
    );
  }

  return {
    seq,
    claimed: response.claimed,
    isolate: response.isolate,
    colo: response.colo,
    delayMs: response.delayMs,
  };
}

export interface Distribution {
  count: number;
  min: number;
  p10: number;
  median: number;
  p90: number;
  max: number;
  mean: number;
}

export function distribution(values: number[]): Distribution | null {
  if (!values.length) return null;
  const sorted = [...values].sort((a, b) => a - b);
  const at = (q: number) => sorted[Math.min(sorted.length - 1, Math.ceil(q * sorted.length) - 1)]!;
  return {
    count: sorted.length,
    min: sorted[0]!,
    p10: at(0.1),
    median: at(0.5),
    p90: at(0.9),
    max: sorted[sorted.length - 1]!,
    mean: sorted.reduce((sum, v) => sum + v, 0) / sorted.length,
  };
}

export interface GapTrial {
  deltaMs: number;
  achievedDeltaMs: number;
  a: RaceOutcome;
  b: RaceOutcome;
}

export type GapExclusion =
  | "zero-claims"
  | "leader-did-not-claim"
  | "mixed-colo"
  | "same-isolate";

type GapClassification = GapExclusion | "decidable";

function classifyGapTrial(trial: GapTrial): GapClassification {
  const { a, b } = trial;
  if (!a.claimed && !b.claimed) return "zero-claims";
  if (!a.claimed) return "leader-did-not-claim";
  if (a.colo !== b.colo) return "mixed-colo";
  if (a.isolate === b.isolate) return "same-isolate";
  return "decidable";
}

export interface GapSummary {
  deltaMs: number;
  attempted: number;
  trials: number;
  decidable: number;
  secondClaims: number;
  secondClaimRate: number | null;
  excluded: Record<GapExclusion, number>;
  achievedDeltaMs: Distribution | null;
}

const noExclusions = (): Record<GapExclusion, number> => ({
  "zero-claims": 0,
  "leader-did-not-claim": 0,
  "mixed-colo": 0,
  "same-isolate": 0,
});

export function summarizeGap(
  deltaMs: number,
  trials: GapTrial[],
  attempted = trials.length,
): GapSummary {
  const excluded = noExclusions();
  const decidable: GapTrial[] = [];
  for (const trial of trials) {
    const verdict = classifyGapTrial(trial);
    if (verdict === "decidable") decidable.push(trial);
    else excluded[verdict] += 1;
  }
  const secondClaims = decidable.filter((t) => t.b.claimed).length;
  return {
    deltaMs,
    attempted,
    trials: trials.length,
    decidable: decidable.length,
    secondClaims,
    secondClaimRate: decidable.length ? secondClaims / decidable.length : null,
    excluded,
    achievedDeltaMs: distribution(trials.map((t) => t.achievedDeltaMs)),
  };
}

export type WindowVerdict =
  | "measured"
  | "below-resolution"
  | "exceeds-sweep"
  | "inconclusive";

export interface WindowResult {
  verdict: WindowVerdict;
  windowMs: number | null;
  threshold: number;
  minDecidable: number;
  undecidedDeltaMs: number[];
}

const SUPPRESSED_RATE = 0.05;
const MIN_DECIDABLE = 20;

export function chooseWindow(
  summaries: GapSummary[],
  options: { threshold?: number; minDecidable?: number } = {},
): WindowResult {
  const threshold = options.threshold ?? SUPPRESSED_RATE;
  const minDecidable = options.minDecidable ?? MIN_DECIDABLE;
  const sorted = [...summaries].sort((a, b) => a.deltaMs - b.deltaMs);
  const undecidedDeltaMs = sorted
    .filter((s) => s.decidable < minDecidable)
    .map((s) => s.deltaMs);

  const base = { threshold, minDecidable, undecidedDeltaMs };
  if (!sorted.length || undecidedDeltaMs.length) {
    return { ...base, verdict: "inconclusive", windowMs: null };
  }

  const suppressed = (s: GapSummary) => s.secondClaimRate! < threshold;
  const index = sorted.findIndex(
    (s, i) => i + 1 < sorted.length && suppressed(s) && suppressed(sorted[i + 1]!),
  );
  if (index === -1) return { ...base, verdict: "exceeds-sweep", windowMs: null };
  return {
    ...base,
    verdict: index === 0 ? "below-resolution" : "measured",
    windowMs: sorted[index]!.deltaMs,
  };
}

export interface BurstTrial {
  size: number;
  jitterMs: number;
  dispersionMs: number;
  outcomes: RaceOutcome[];
}

interface TrialEscapes {
  rawClaims: number;
  escapes: number;
  lateEscapes: number | null;
  distinctIsolates: number;
  distinctColos: number;
}

function escapesOf(outcomes: RaceOutcome[], windowMs: number | null): TrialEscapes {
  const claimed = outcomes.filter((o) => o.claimed);
  const firstDrawByIsolate = new Map<string, number>();
  for (const outcome of claimed) {
    const seen = firstDrawByIsolate.get(outcome.isolate);
    if (seen === undefined || outcome.delayMs < seen) {
      firstDrawByIsolate.set(outcome.isolate, outcome.delayMs);
    }
  }
  const draws = [...firstDrawByIsolate.values()].sort((a, b) => a - b);
  const earliest = draws[0];
  return {
    rawClaims: claimed.length,
    escapes: firstDrawByIsolate.size,
    lateEscapes:
      windowMs === null || earliest === undefined
        ? null
        : draws.filter((draw) => draw > earliest + windowMs).length,
    distinctIsolates: new Set(outcomes.map((o) => o.isolate)).size,
    distinctColos: new Set(outcomes.map((o) => o.colo)).size,
  };
}

type BurstClassification =
  | "zero-claims"
  | "mixed-colo"
  | "single-isolate"
  | "counted";

function classifyBurstTrial({
  rawClaims,
  distinctColos,
  distinctIsolates,
}: TrialEscapes): BurstClassification {
  if (rawClaims === 0) return "zero-claims";
  if (distinctColos !== 1) return "mixed-colo";
  if (distinctIsolates === 1) return "single-isolate";
  return "counted";
}

export interface BurstSummary {
  size: number;
  jitterMs: number;
  attempted: number;
  trials: number;
  counted: number;
  singleIsolateTrials: number;
  singleIsolateRate: number | null;
  discarded: { "zero-claims": number; "mixed-colo": number };
  escapes: Distribution | null;
  lateEscapes: Distribution | null;
  rawClaims: Distribution | null;
  distinctIsolates: Distribution | null;
  dispersionMs: Distribution | null;
  delayMs: Distribution | null;
  lowerBound: boolean;
}

export function summarizeBurst(
  size: number,
  trials: BurstTrial[],
  windowMs: number | null,
  attempted = trials.length,
  jitterMs = 0,
): BurstSummary {
  const discarded = { "zero-claims": 0, "mixed-colo": 0 };
  let singleIsolateTrials = 0;
  const counted: { trial: BurstTrial; escapes: TrialEscapes }[] = [];
  for (const trial of trials) {
    const escapes = escapesOf(trial.outcomes, windowMs);
    const verdict = classifyBurstTrial(escapes);
    if (verdict === "counted") counted.push({ trial, escapes });
    else if (verdict === "single-isolate") singleIsolateTrials += 1;
    else discarded[verdict] += 1;
  }

  const dispersion = distribution(counted.map(({ trial }) => trial.dispersionMs));
  const late = counted
    .map(({ escapes }) => escapes.lateEscapes)
    .filter((value): value is number => value !== null);
  return {
    size,
    jitterMs,
    attempted,
    trials: trials.length,
    counted: counted.length,
    singleIsolateTrials,
    singleIsolateRate: trials.length ? singleIsolateTrials / trials.length : null,
    discarded,
    escapes: distribution(counted.map(({ escapes }) => escapes.escapes)),
    lateEscapes: distribution(late),
    rawClaims: distribution(counted.map(({ escapes }) => escapes.rawClaims)),
    distinctIsolates: distribution(counted.map(({ escapes }) => escapes.distinctIsolates)),
    dispersionMs: dispersion,
    delayMs: distribution(counted.flatMap(({ trial }) => trial.outcomes.map((o) => o.delayMs))),
    lowerBound: windowMs === null || (dispersion !== null && dispersion.p90 > windowMs),
  };
}

export type JitterVerdict =
  | "none-requested"
  | "spread"
  | "degenerate";

export function jitterVerdict(jitterMs: number, delayMs: Distribution | null): JitterVerdict {
  if (jitterMs === 0) return "none-requested";
  if (delayMs === null) return "degenerate";
  const midpoint = jitterMs / 2;
  return delayMs.max >= midpoint && delayMs.min <= midpoint ? "spread" : "degenerate";
}

export interface SizingInputs {
  windowSeconds: number;
  isolatesPerColo: number;
  colos: number;
  sentinelTtlSeconds: number;
}

export interface SizingRow {
  lambdaPerColo: number;
  escapesPerColo: number;
  fanInPerStaleEvent: number;
  sustainedRequestsPerSecond: number;
  cappedByIsolates: boolean;
}

export function sizingTable(inputs: SizingInputs, lambdas: number[]): SizingRow[] {
  return lambdas.map((lambdaPerColo) => {
    const uncapped = 1 + lambdaPerColo * inputs.windowSeconds;
    const escapesPerColo = Math.min(uncapped, inputs.isolatesPerColo);
    const fanInPerStaleEvent = inputs.colos * escapesPerColo;
    return {
      lambdaPerColo,
      escapesPerColo,
      fanInPerStaleEvent,
      sustainedRequestsPerSecond: fanInPerStaleEvent / inputs.sentinelTtlSeconds,
      cappedByIsolates: uncapped > inputs.isolatesPerColo,
    };
  });
}
