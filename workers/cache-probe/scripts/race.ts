// Drives the race against the deployed probe. Run it from a machine outside
// Cloudflare.
//
//   node scripts/race.ts --base https://probe.example.com --phase control
//   node scripts/race.ts --base https://probe.example.com --phase gap   --trials 200
//   node scripts/race.ts --base https://probe.example.com --phase burst --trials 100 \
//     --sizes 2,8,32,128 --window <the gap phase's W in ms>
//
// Three rules this script exists to enforce, and the reasons a run is worthless
// without them:
//
//   Nothing is ever retried. A resend against a key the first send just claimed
//   reports claimed:false and manufactures a suppression that never happened.
//   Any non-200, redirect or network error discards the WHOLE trial, and the
//   discards are counted.
//
//   Every racer gets its own pre-warmed socket. TCP+TLS handshakes cost tens of
//   milliseconds — the same order as the quantity being measured — so a burst on
//   cold connections spreads its own arrivals wider than the window and
//   under-reports escapes.
//
//   The gap sweep never differences a Worker's clock against anything. The delay
//   between two racers is imposed here, on one clock, and both racers pay the
//   same round trip, so the driver's own latency cancels out of it. That is the
//   whole reason the sweep exists rather than a single timed write.
//
// See README.md for the phases and for why the base URL must not be workers.dev.

import { randomUUID } from "node:crypto";
import { mkdir, writeFile } from "node:fs/promises";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

import { Client } from "undici";

import {
  chooseWindow,
  distribution,
  sizingTable,
  summarizeBurst,
  summarizeGap,
  type BurstTrial,
  type GapSummary,
  type GapTrial,
  type RaceOutcome,
} from "../src/race-analysis.ts";

const sleep = (ms: number) => new Promise((done) => setTimeout(done, ms));

class Abort extends Error {}

// ---------------------------------------------------------------------------
// Options

interface RaceOptions {
  base: string;
  phase: "control" | "gap" | "burst";
  trials: number;
  deltas: number[];
  sizes: number[];
  sockets: number;
  scope: "onzone" | "offzone";
  windowMs: number | null;
  colos: number;
  sentinelTtlSeconds: number;
  out: string;
}

const DEFAULT_DELTAS = [0, 10, 25, 50, 100, 150, 200, 300, 500, 1_000];
const DEFAULT_SIZES = [2, 8, 32, 128];

function parseOptions(argv: string[], defaultOut: string): RaceOptions {
  const flags = new Map<string, string>();
  for (let i = 0; i < argv.length; i += 2) {
    const flag = argv[i];
    if (!flag?.startsWith("--")) throw new Abort(`unexpected argument: ${flag}`);
    const value = argv[i + 1];
    if (value === undefined || value.startsWith("--")) {
      throw new Abort(`${flag} requires a value`);
    }
    flags.set(flag.slice(2), value);
  }

  const base = flags.get("base");
  if (!base) throw new Abort("--base <https://probe.example.com> is required");
  const phase = flags.get("phase") ?? "control";
  if (phase !== "control" && phase !== "gap" && phase !== "burst") {
    throw new Abort(`--phase must be control, gap or burst, got "${phase}"`);
  }
  const scope = flags.get("scope") ?? "offzone";
  if (scope !== "onzone" && scope !== "offzone") {
    throw new Abort(`--scope must be onzone or offzone, got "${scope}"`);
  }
  const number = (name: string, fallback: number, min = Number.MIN_VALUE) => {
    const raw = flags.get(name);
    if (raw === undefined) return fallback;
    const parsed = Number(raw);
    if (!Number.isFinite(parsed) || parsed < min) {
      throw new Abort(`--${name} must be a number >= ${min}, got "${raw}"`);
    }
    return parsed;
  };
  const list = (name: string, fallback: number[]) => {
    const raw = flags.get(name);
    if (raw === undefined) return fallback;
    return raw.split(",").map((part) => {
      const parsed = Number(part);
      if (!Number.isFinite(parsed) || parsed < 0) {
        throw new Abort(`--${name} must be non-negative numbers, got "${part}"`);
      }
      return parsed;
    });
  };

  return {
    base: base.replace(/\/$/, ""),
    phase,
    scope,
    trials: number("trials", phase === "gap" ? 200 : 100),
    deltas: list("deltas", DEFAULT_DELTAS),
    sizes: list("sizes", DEFAULT_SIZES),
    // Two racers are the minimum a gap trial can pair, and a pool of one can
    // never produce a cross-isolate pair at all.
    sockets: Math.max(2, number("sockets", 16)),
    // A window of zero is a real answer — the claim is colo-visible at once —
    // so it must be expressible, which a positive-only flag would forbid.
    windowMs: flags.has("window") ? number("window", 0, 0) : null,
    colos: number("colos", 300),
    sentinelTtlSeconds: number("sentinelTtl", 5),
    out: flags.get("out") ?? defaultOut,
  };
}

// ---------------------------------------------------------------------------
// Transport. One Client is one socket. A bare Client neither retries nor
// follows redirects — undici puts both behind opt-in interceptors, and neither
// is opted into here — so any anomaly surfaces as a non-200 or a thrown error,
// and the caller discards the whole trial for it. Anything that repaired a trial
// by re-sending would be sending against a key the first send just claimed.

const openClient = (origin: string) => new Client(origin, { pipelining: 1 });

async function call<T>(client: Client, method: "GET" | "POST", path: string): Promise<T> {
  const response = await client.request({ path, method });
  const body = await response.body.text();
  if (response.statusCode !== 200) {
    throw new Error(`${method} ${path} -> ${response.statusCode}: ${body.slice(0, 120)}`);
  }
  return JSON.parse(body) as T;
}

interface Identity {
  isolate: string;
  colo: string | null;
  host: string;
}

// A socket that has completed TCP+TLS and been proven live. Firing a race on a
// cold socket would put the handshake inside the measurement.
async function openRacers(origin: string, count: number): Promise<Client[]> {
  const clients = Array.from({ length: count }, () => openClient(origin));
  await Promise.all(clients.map((client) => call<Identity>(client, "GET", "/identity")));
  return clients;
}

const closeRacers = (clients: Client[]) => Promise.all(clients.map((c) => c.close()));

// ---------------------------------------------------------------------------
// Phase 0 — the key-scope control. Production's colo-cache keys all sit on
// synthetic hostnames belonging to no zone; PR 1 only ever proved an ON-zone
// key. Until both arms come back visible, every later number is about a cache
// that may not be storing anything.

interface ControlArm {
  scope: string;
  writer: string;
  writerColo: string | null;
  verified: boolean;
  reads: number;
  hits: number;
  crossIsolateReads: number;
  crossIsolateHits: number;
  readerIsolates: number;
  visible: boolean;
}

async function controlArm(
  origin: string,
  run: string,
  scope: string,
  fanout: number,
): Promise<ControlArm> {
  const writerClient = openClient(origin);
  const written = await call<Identity & { verified: boolean }>(
    writerClient,
    "GET",
    `/control?run=${run}&scope=${scope}&mode=write`,
  );
  await writerClient.close();

  const readers = await openRacers(origin, fanout);
  const reads = await Promise.all(
    readers.map((client, seq) =>
      call<Identity & { hit: boolean; writer: string | null }>(
        client,
        "GET",
        `/control?run=${run}&scope=${scope}&mode=read&seq=${seq}`,
      ),
    ),
  );
  await closeRacers(readers);

  const sameColo = reads.filter((r) => r.colo === written.colo);
  const cross = sameColo.filter((r) => r.isolate !== written.isolate);
  const crossHits = cross.filter((r) => r.hit);
  return {
    scope,
    writer: written.isolate,
    writerColo: written.colo,
    verified: written.verified,
    reads: reads.length,
    hits: reads.filter((r) => r.hit).length,
    crossIsolateReads: cross.length,
    crossIsolateHits: crossHits.length,
    readerIsolates: new Set(sameColo.map((r) => r.isolate)).size,
    visible: written.verified && crossHits.length > 0,
  };
}

type ControlVerdict =
  // Both key shapes store and are colo-visible: production's synthetic
  // hostnames work and the window is worth measuring.
  | "both-scopes-visible"
  // The on-zone key works and the production shape does not. Every colo-cache
  // tier in workers/nextjs is inert, silently, failing open. This outranks the
  // window entirely.
  | "offzone-inert"
  // Nothing stored anywhere: workers.dev, or the route is not live.
  | "cache-inert"
  // No read ever reached an isolate other than the writer's, so neither arm can
  // be told apart from the other.
  | "inconclusive";

function controlVerdict(onzone: ControlArm, offzone: ControlArm): ControlVerdict {
  if (!onzone.verified && !offzone.verified) return "cache-inert";
  if (onzone.crossIsolateReads === 0 || offzone.crossIsolateReads === 0) {
    return "inconclusive";
  }
  if (onzone.visible && offzone.visible) return "both-scopes-visible";
  if (onzone.visible) return "offzone-inert";
  return "inconclusive";
}

async function control(options: RaceOptions, origin: string) {
  const run = `ctl-${randomUUID()}`;
  const onzone = await controlArm(origin, `${run}-on`, "onzone", options.sockets);
  const offzone = await controlArm(origin, `${run}-off`, "offzone", options.sockets);
  return { run, onzone, offzone, verdict: controlVerdict(onzone, offzone) };
}

// ---------------------------------------------------------------------------
// Preflight. Every gate here is fatal, not a warning: each of them turns a green
// run into a fabrication that looks like an answer.

interface Preflight {
  host: string;
  colo: string | null;
  control: Awaited<ReturnType<typeof control>>;
  cleanBurst: number;
  rttMs: ReturnType<typeof distribution>;
}

async function preflight(options: RaceOptions, origin: string): Promise<Preflight> {
  const host = new URL(options.base).host;
  if (host.endsWith("workers.dev")) {
    throw new Abort(
      `${host} is a workers.dev subdomain, where caches.default is inert. Every ` +
        "racer would claim, E would equal N, and the run would read as a maximal\n" +
        "alarming result that measured nothing. Deploy to a zone route.",
    );
  }

  const measured = await control(options, origin);
  if (measured.verdict !== "both-scopes-visible") {
    throw new Abort(
      `the key-scope control returned ${measured.verdict}, not both-scopes-visible.\n` +
        `${JSON.stringify(measured, null, 2)}`,
    );
  }
  if (!(options.scope === "onzone" ? measured.onzone : measured.offzone).visible) {
    throw new Abort(`the ${options.scope} key is not colo-visible; nothing to race for`);
  }

  // A zone carrying a wildcard worker route can serve a fraction of requests
  // from a foreign script for minutes after a deploy. In a race that arrives as
  // a non-200, which is loud — but only if it is not already poisoning trials.
  const clients = await openRacers(origin, 20);
  const clean = await Promise.all(
    Array.from({ length: 200 }, (_, i) => call<Identity>(clients[i % 20]!, "GET", "/identity")),
  );

  // The round trip PR 1 never recorded, taken sequentially on one warm socket so
  // it is a latency and not a queueing delay.
  const latencies: number[] = [];
  for (let i = 0; i < 40; i += 1) {
    const started = performance.now();
    await call<Identity>(clients[0]!, "GET", "/identity");
    latencies.push(performance.now() - started);
  }
  await closeRacers(clients);

  return {
    host,
    colo: clean[0]?.colo ?? null,
    control: measured,
    cleanBurst: clean.length,
    rttMs: distribution(latencies),
  };
}

// ---------------------------------------------------------------------------
// Phase 1 — the gap sweep.

interface RaceResponse extends Identity {
  claimed: boolean;
  key: string;
  scope: string;
  seq: string | null;
}

function outcomeOf(response: RaceResponse, key: string, seq: number): RaceOutcome {
  if (response.key !== key || response.seq !== String(seq)) {
    throw new Abort(
      `racer ${seq} on key ${key} was answered for key ${response.key} seq ` +
        `${response.seq}. One racer received another's body; the run is void.`,
    );
  }
  return {
    seq,
    claimed: response.claimed,
    isolate: response.isolate,
    colo: response.colo ?? "unknown",
  };
}

const racePath = (options: RaceOptions, key: string, seq: number) =>
  `/race?key=${key}&seq=${seq}&scope=${options.scope}&ttl=${options.sentinelTtlSeconds}`;

async function gapSweep(options: RaceOptions, origin: string) {
  const summaries: GapSummary[] = [];
  const trialsByDelta: Record<number, GapTrial[]> = {};
  let discarded = 0;

  for (const deltaMs of options.deltas) {
    let racers = await openRacers(origin, options.sockets);
    const trials: GapTrial[] = [];

    for (let trial = 0; trial < options.trials; trial += 1) {
      // Never reused: a warm key would report the follower suppressed by a
      // claim from an earlier trial rather than by this one's leader.
      const key = `race-${randomUUID()}`;
      // Rotate both ends of the pair: a fixed pair of sockets would measure
      // whatever isolates those two sockets happen to be pinned to.
      const first = trial % racers.length;
      const offset = 1 + (Math.floor(trial / racers.length) % (racers.length - 1));
      const second = (first + offset) % racers.length;

      try {
        const sentA = performance.now();
        const a = call<RaceResponse>(racers[first]!, "POST", racePath(options, key, 0));
        await sleep(Math.max(0, deltaMs - (performance.now() - sentA)));
        const sentB = performance.now();
        const b = call<RaceResponse>(racers[second]!, "POST", racePath(options, key, 1));
        const [first_, second_] = await Promise.all([a, b]);

        trials.push({
          deltaMs,
          achievedDeltaMs: sentB - sentA,
          a: outcomeOf(first_, key, 0),
          b: outcomeOf(second_, key, 1),
        });
      } catch (error) {
        if (error instanceof Abort) throw error;
        discarded += 1;
        // A pool that produced an error is not trusted again: replacing it keeps
        // later trials on warm connections rather than silently going cold.
        await closeRacers(racers).catch(() => {});
        racers = await openRacers(origin, options.sockets);
      }
    }

    await closeRacers(racers);
    const summary = summarizeGap(deltaMs, trials);
    if (summary.excluded["zero-claims"] > 0) {
      throw new Abort(
        `${summary.excluded["zero-claims"]} trial(s) at delta ${deltaMs}ms had no claim ` +
          "at all on a key nobody had written. The instrument is broken, not the cache.",
      );
    }
    summaries.push(summary);
    trialsByDelta[deltaMs] = trials;
    console.log(
      `  delta ${String(deltaMs).padStart(5)}ms  decidable ${String(summary.decidable).padStart(4)}` +
        `  second claimed ${summary.secondClaims}` +
        `  rate ${summary.secondClaimRate === null ? "n/a" : summary.secondClaimRate.toFixed(4)}`,
    );
  }

  return { summaries, trialsByDelta, discarded, window: chooseWindow(summaries) };
}

// ---------------------------------------------------------------------------
// Phase 2 — the burst.

async function burstSweep(options: RaceOptions, origin: string) {
  const summaries = [];
  const trialsBySize: Record<number, BurstTrial[]> = {};
  let discarded = 0;

  for (const size of options.sizes) {
    let racers = await openRacers(origin, size);
    const trials: BurstTrial[] = [];

    for (let trial = 0; trial < options.trials; trial += 1) {
      const key = `race-${randomUUID()}`;
      const sentAt: number[] = [];
      try {
        const responses = await Promise.all(
          racers.map((client, seq) => {
            sentAt.push(performance.now());
            return call<RaceResponse>(client, "POST", racePath(options, key, seq));
          }),
        );
        trials.push({
          size,
          dispersionMs: Math.max(...sentAt) - Math.min(...sentAt),
          outcomes: responses.map((response, seq) => outcomeOf(response, key, seq)),
        });
      } catch (error) {
        if (error instanceof Abort) throw error;
        discarded += 1;
        await closeRacers(racers).catch(() => {});
        racers = await openRacers(origin, size);
      }
    }

    await closeRacers(racers);
    const summary = summarizeBurst(size, trials, options.windowMs);
    if (summary.invariantViolations > 0) {
      throw new Abort(
        `${summary.invariantViolations} trial(s) at N=${size} did not account for their ` +
          "racers one-for-one. The instrument is double-counting; the run is void.",
      );
    }
    if (summary.discarded["zero-claims"] > 0) {
      throw new Abort(
        `${summary.discarded["zero-claims"]} trial(s) at N=${size} had no claim at all on a ` +
          "cold key. The instrument is broken, not the cache.",
      );
    }
    summaries.push(summary);
    trialsBySize[size] = trials;
    console.log(
      `  N=${String(size).padStart(4)}  counted ${String(summary.counted).padStart(4)}` +
        `  escapes median ${summary.escapes?.median ?? "n/a"}` +
        ` (p10 ${summary.escapes?.p10 ?? "n/a"}, p90 ${summary.escapes?.p90 ?? "n/a"},` +
        ` max ${summary.escapes?.max ?? "n/a"})` +
        `  isolates median ${summary.distinctIsolates?.median ?? "n/a"}` +
        `  dispersion median ${summary.dispersionMs?.median.toFixed(1) ?? "n/a"}ms` +
        (summary.lowerBound ? "  [LOWER BOUND]" : ""),
    );
    if ((summary.singleIsolateRate ?? 0) > 0.1) {
      console.log(
        `        ${summary.singleIsolateTrials}/${summary.trials} trials landed on ONE isolate: ` +
          "that fraction measured L0, not L1.",
      );
    }
  }

  return { summaries, trialsBySize, discarded };
}

// ---------------------------------------------------------------------------

async function main() {
  const options = parseOptions(
    process.argv.slice(2),
    resolve(dirname(fileURLToPath(import.meta.url)), `../runs/race-${Date.now()}.json`),
  );
  const origin = new URL(options.base).origin;

  const report: Record<string, unknown> = {
    startedAt: new Date().toISOString(),
    options,
  };

  if (options.phase === "control") {
    const host = new URL(options.base).host;
    if (host.endsWith("workers.dev")) {
      throw new Abort(`${host} is a workers.dev subdomain, where caches.default is inert.`);
    }
    const measured = await control(options, origin);
    report.control = measured;
    console.log(`\nkey-scope control: ${measured.verdict}`);
    for (const arm of [measured.onzone, measured.offzone]) {
      console.log(
        `  ${arm.scope.padEnd(8)} verified ${arm.verified}` +
          `  cross-isolate hits ${arm.crossIsolateHits}/${arm.crossIsolateReads}` +
          `  reader isolates ${arm.readerIsolates}  colo ${arm.writerColo}`,
      );
    }
    if (measured.verdict === "offzone-inert") {
      console.log(
        "\n  STOP. The on-zone key stores and is colo-visible; the production key shape\n" +
          "  (https://refresh.ocel/…) is not. Every colo-cache tier in workers/nextjs is\n" +
          "  keyed on a synthetic hostname, so L1, the entry tier, the tag-clock front and\n" +
          "  the image tier are all no-ops in production — silently, failing open.",
      );
    }
  } else {
    const checks = await preflight(options, origin);
    report.preflight = checks;
    console.log(
      `\npreflight ok: ${checks.host} in ${checks.colo}, ${checks.cleanBurst} clean requests, ` +
        `rtt median ${checks.rttMs?.median.toFixed(1)}ms (p10 ${checks.rttMs?.p10.toFixed(1)}ms)`,
    );

    if (options.phase === "gap") {
      console.log(`\ngap sweep (${options.trials} trials per delta, scope ${options.scope})`);
      const sweep = await gapSweep(options, origin);
      report.gap = sweep;
      console.log(
        `\nwindow: ${sweep.window.verdict}` +
          (sweep.window.windowMs === null ? "" : ` at ${sweep.window.windowMs}ms`) +
          `  (discarded trials: ${sweep.discarded})`,
      );
    } else {
      console.log(`\nburst (${options.trials} trials per size, scope ${options.scope})`);
      const sweep = await burstSweep(options, origin);
      report.burst = sweep;
      console.log(`\ndiscarded trials: ${sweep.discarded}`);

      const isolatesPerColo = Math.max(
        99,
        ...sweep.summaries.map((s) => s.distinctIsolates?.max ?? 0),
      );
      if (options.windowMs !== null) {
        const table = sizingTable(
          {
            windowSeconds: options.windowMs / 1_000,
            isolatesPerColo,
            colos: options.colos,
            sentinelTtlSeconds: options.sentinelTtlSeconds,
          },
          [1, 10, 100, 1_000],
        );
        report.sizing = { isolatesPerColo, rows: table };
        console.log(
          `\nL2 sizing at W=${options.windowMs}ms, I_colo>=${isolatesPerColo}, ` +
            `C=${options.colos}, ttl=${options.sentinelTtlSeconds}s ` +
            "(lambda is an OPERATOR PARAMETER, not measured here)",
        );
        for (const row of table) {
          console.log(
            `  lambda ${String(row.lambdaPerColo).padStart(5)} rps  E ${row.escapesPerColo.toFixed(2)}` +
              `${row.cappedByIsolates ? " (capped by isolates)" : ""}` +
              `  F ${row.fanInPerStaleEvent.toFixed(0)}  R ${row.sustainedRequestsPerSecond.toFixed(0)} rps`,
          );
        }
      } else {
        console.log(
          "\nno --window given, so no sizing table: R is a function of W and guessing it\n" +
            "is exactly what this measurement exists to stop.",
        );
      }
    }
  }

  await mkdir(dirname(options.out), { recursive: true });
  await writeFile(options.out, JSON.stringify(report, null, 2));
  console.log(`\nraw observations: ${options.out}`);
}

try {
  await main();
} catch (error) {
  if (!(error instanceof Abort)) throw error;
  console.error(`\nABORTED: ${error.message}\n`);
  process.exitCode = 1;
}
