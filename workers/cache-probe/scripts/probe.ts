// Drives the deployed probe and writes down what it saw. Run it from a machine
// outside Cloudflare: every elapsed time is measured against this process's
// clock, which is the only clock in the system that is allowed to be differenced.
//
//   node scripts/probe.ts --base https://probe.example.com
//
// See README.md for the full flag set and for why the base URL must not be a
// workers.dev subdomain.

import { mkdir, writeFile } from "node:fs/promises";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

import {
  summarizeIsolates,
  summarizeSentinel,
  summarizeTtl,
  type IdentitySample,
  type SentinelObservation,
  type TtlObservation,
} from "../src/analysis.ts";

interface Options {
  base: string;
  concurrency: number;
  rounds: number;
  sentinelSeconds: number;
  ttls: number[];
  pollSeconds: number;
  pollFanout: number;
  windowSeconds: number;
  out: string;
}

const defaults: Omit<Options, "base" | "out"> = {
  concurrency: 64,
  rounds: 4,
  sentinelSeconds: 60,
  ttls: [10],
  pollSeconds: 1,
  pollFanout: 8,
  windowSeconds: 180,
};

function parseArgs(argv: string[]): Options {
  const flags = new Map<string, string>();
  for (let i = 0; i < argv.length; i += 2) {
    if (!argv[i]?.startsWith("--")) throw new Error(`unexpected argument: ${argv[i]}`);
    flags.set(argv[i]!.slice(2), argv[i + 1] ?? "");
  }
  const base = flags.get("base");
  if (!base) throw new Error("--base <https://probe.example.com> is required");
  const number = (name: keyof typeof defaults) =>
    flags.has(name) ? Number(flags.get(name)) : (defaults[name] as number);

  return {
    base: base.replace(/\/$/, ""),
    concurrency: number("concurrency"),
    rounds: number("rounds"),
    sentinelSeconds: number("sentinelSeconds"),
    pollSeconds: number("pollSeconds"),
    pollFanout: number("pollFanout"),
    windowSeconds: number("windowSeconds"),
    ttls: flags.has("ttls")
      ? flags.get("ttls")!.split(",").map(Number)
      : defaults.ttls,
    out:
      flags.get("out") ??
      resolve(dirname(fileURLToPath(import.meta.url)), "../runs/probe.json"),
  };
}

const sleep = (ms: number) => new Promise((done) => setTimeout(done, ms));

async function getJson<T>(url: string, init?: RequestInit): Promise<T> {
  const response = await fetch(url, init);
  if (!response.ok) {
    throw new Error(`${init?.method ?? "GET"} ${url} -> ${response.status}`);
  }
  return (await response.json()) as T;
}

const burst = <T>(count: number, call: () => Promise<T>) =>
  Promise.all(Array.from({ length: count }, call));

interface Identity {
  isolate: string;
  colo: string | null;
  host: string;
}

interface EntryRead extends Identity {
  hit: boolean;
  writer: string | null;
  age: string | null;
  lookupMs: number;
}

async function census(options: Options): Promise<IdentitySample[]> {
  const samples: IdentitySample[] = [];
  for (let round = 0; round < options.rounds; round += 1) {
    const identities = await burst(options.concurrency, () =>
      getJson<Identity>(`${options.base}/identity`),
    );
    samples.push(
      ...identities.map((i) => ({ colo: i.colo ?? "unknown", isolate: i.isolate })),
    );
    await sleep(250);
  }
  return samples;
}

async function sentinel(options: Options, run: string) {
  const written = await getJson<Identity>(
    `${options.base}/entry?run=${run}&ttl=${options.sentinelSeconds}`,
    { method: "PUT" },
  );
  const writtenAt = Date.now();

  const observations: SentinelObservation[] = [];
  // Read for a while: the answer being looked for is whether a *later* isolate
  // sees the write, so the burst has to be wide enough to land somewhere else
  // and repeated long enough for any propagation to finish.
  for (let round = 0; round < options.rounds * 2; round += 1) {
    const reads = await burst(options.concurrency, () =>
      getJson<EntryRead>(`${options.base}/entry?run=${run}`),
    );
    const elapsedMs = Date.now() - writtenAt;
    observations.push(
      ...reads.map((read) => ({
        reader: read.isolate,
        colo: read.colo ?? "unknown",
        writer: read.writer,
        hit: read.hit,
        elapsedMs,
      })),
    );
    await sleep(500);
  }

  return {
    writer: written.isolate,
    writerColo: written.colo,
    observations,
    summary: summarizeSentinel(written.isolate, observations),
  };
}

async function ttl(options: Options, run: string, ttlSeconds: number) {
  await getJson(`${options.base}/entry?run=${run}&ttl=${ttlSeconds}`, { method: "PUT" });
  const writtenAt = Date.now();

  const observations: TtlObservation[] = [];
  const deadline = writtenAt + options.windowSeconds * 1_000;
  while (Date.now() < deadline) {
    await sleep(options.pollSeconds * 1_000);
    // A fan-out per poll, counted as a hit if any read hit: if the cache turns
    // out to be isolate-local, a single read would report a miss for a live
    // entry and the lifetime measurement would be wrong rather than unknown.
    const reads = await burst(options.pollFanout, () =>
      getJson<EntryRead>(`${options.base}/entry?run=${run}`),
    );
    const hit = reads.some((read) => read.hit);
    observations.push({ hit, elapsedMs: Date.now() - writtenAt });
    // Two consecutive misses end the poll: one miss could be a read that raced
    // an eviction that a later read disproves, and the analysis treats it that
    // way, so the run stops only once the entry looks durably gone.
    if (observations.slice(-2).every((o) => !o.hit) && observations.length >= 2) break;
  }

  return { ttlSeconds, observations, summary: summarizeTtl(ttlSeconds, observations) };
}

async function main() {
  const options = parseArgs(process.argv.slice(2));
  const host = new URL(options.base).host;
  if (host.endsWith("workers.dev")) {
    console.warn(
      `WARNING: ${host} is a workers.dev subdomain, where the Cache API is a no-op.\n` +
        "         Every measurement below will read as never-cached regardless of\n" +
        "         Cloudflare's real behaviour. Deploy to a zone route first.\n",
    );
  }

  const runId = `probe-${Date.now()}`;
  const report = {
    runId,
    base: options.base,
    startedAt: new Date().toISOString(),
    options,
    isolates: summarizeIsolates(await census(options)),
    sentinel: await sentinel(options, `${runId}-sentinel`),
    ttl: [] as Awaited<ReturnType<typeof ttl>>[],
  };
  for (const seconds of options.ttls) {
    report.ttl.push(await ttl(options, `${runId}-ttl-${seconds}`, seconds));
  }

  await mkdir(dirname(options.out), { recursive: true });
  await writeFile(options.out, JSON.stringify(report, null, 2));

  console.log(`\nisolates per colo`);
  for (const row of report.isolates) {
    console.log(`  ${row.colo}: ${row.isolates} isolates over ${row.samples} requests`);
  }
  console.log(`\nsentinel (writer ${report.sentinel.writer} in ${report.sentinel.writerColo})`);
  console.log(`  ${JSON.stringify(report.sentinel.summary, null, 2).replace(/\n/g, "\n  ")}`);
  for (const measured of report.ttl) {
    console.log(`\nttl ${measured.ttlSeconds}s`);
    console.log(`  ${JSON.stringify(measured.summary, null, 2).replace(/\n/g, "\n  ")}`);
  }
  console.log(`\nraw observations: ${options.out}`);
}

await main();
