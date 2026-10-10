import assert from "node:assert/strict";
import { randomUUID } from "node:crypto";
import type dns from "node:dns";
import https from "node:https";
import { marker } from "../html";
import type { Check, CheckContext } from "./context";

const DNS_OVER_HTTPS = "https://dns.google/resolve";
const A_RECORD = 1;
const VANTAGES = [
  { region: "Europe (Ireland)", subnet: "52.208.0.0/24" },
  { region: "Asia Pacific (Tokyo)", subnet: "13.112.0.0/24" },
];
const BURST = 8;
const REQUEST_TIMEOUT_MS = 60_000;
const POP_HEADER = "x-amz-cf-pop";

type DnsAnswer = { Answer?: { type: number; data: string }[] };
type Answer = { status: number; body: string; pop: string };
type LookupCallback = (
  error: NodeJS.ErrnoException | null,
  address: string | dns.LookupAddress[],
  family?: number,
) => void;

export function addressesOf(answer: DnsAnswer): string[] {
  return (answer.Answer ?? []).filter((one) => one.type === A_RECORD).map((one) => one.data);
}

export function rendersOf(bodies: string[]): string[] {
  return bodies.map((html) => marker(html, "shield:cached"));
}

export function settledInOneRender(bodies: string[]): boolean {
  return new Set(rendersOf(bodies)).size === 1;
}

export function airportOf(pop: string): string {
  return pop.slice(0, 3).toUpperCase();
}

export function sharedAirports(popsByVantage: string[][]): string[] {
  const [first = [], ...rest] = popsByVantage.map((pops) => new Set(pops.map(airportOf)));
  return [...first].filter((airport) => rest.some((airports) => airports.has(airport)));
}

export function lookupAt(address: string) {
  return (_hostname: string, options: dns.LookupOptions, done: LookupCallback): void => {
    if (options?.all === true) {
      done(null, [{ address, family: 4 }]);
      return;
    }
    done(null, address, 4);
  };
}

async function edgeAddressFor(hostname: string, subnet: string): Promise<string> {
  const url = new URL(DNS_OVER_HTTPS);
  url.searchParams.set("name", hostname);
  url.searchParams.set("type", "A");
  url.searchParams.set("edns_client_subnet", subnet);
  const res = await fetch(url);
  assert.equal(res.status, 200, `resolving ${hostname} for ${subnet} answered ${res.status}`);
  const [address] = addressesOf((await res.json()) as DnsAnswer);
  assert.ok(address, `${hostname} resolved to no address for a client in ${subnet}`);
  return address;
}

function getVia(address: string, target: URL): Promise<Answer> {
  return new Promise((resolve, reject) => {
    const req = https.request(
      {
        host: target.hostname,
        servername: target.hostname,
        path: `${target.pathname}${target.search}`,
        method: "GET",
        timeout: REQUEST_TIMEOUT_MS,
        lookup: lookupAt(address) as never,
      },
      (res) => {
        const chunks: Buffer[] = [];
        res.on("data", (chunk: Buffer) => chunks.push(chunk));
        res.on("end", () =>
          resolve({
            status: res.statusCode ?? 0,
            body: Buffer.concat(chunks).toString("utf8"),
            pop: String(res.headers[POP_HEADER] ?? ""),
          }),
        );
      },
    );
    req.on("timeout", () => req.destroy(new Error(`${target} through ${address} timed out`)));
    req.on("error", reject);
    req.end();
  });
}

async function burstFromTwoRegions(ctx: CheckContext): Promise<string[]> {
  const base = new URL(ctx.baseUrl);
  const target = new URL(`/cache/shield/${randomUUID()}`, base);
  const addresses = await Promise.all(
    VANTAGES.map((vantage) => edgeAddressFor(base.hostname, vantage.subnet)),
  );
  assert.equal(
    new Set(addresses).size,
    addresses.length,
    `${VANTAGES.map((one) => one.region).join(" and ")} resolved ${base.hostname} to the same edge ${addresses[0]}, so the burst would come from one edge location`,
  );
  const answers = await Promise.all(
    addresses.map((address) =>
      Promise.all(Array.from({ length: BURST }, () => getVia(address, target))),
    ),
  );
  for (const answer of answers.flat()) {
    assert.equal(answer.status, 200, `${target} answered ${answer.status}`);
    assert.ok(
      answer.pop,
      `${target} answered with no ${POP_HEADER} header, so the edge location that served it is unknown`,
    );
  }
  const popsByVantage = answers.map((burst) => burst.map((answer) => answer.pop));
  const shared = sharedAirports(popsByVantage);
  assert.deepEqual(
    shared,
    [],
    `${VANTAGES.map((one) => one.region).join(" and ")} were both served from the edge locations at ${shared.join(", ")} (${popsByVantage.map((pops) => [...new Set(pops)].join(" ")).join(" / ")}), so the burst never came from two regions`,
  );
  return answers.flat().map((answer) => answer.body);
}

export const cloudfrontShieldChecks: Check[] = [
  {
    title:
      "an uncached prerender requested at once from two regions is rendered by one function invocation",
    run: async (ctx) => {
      const bodies = await burstFromTwoRegions(ctx);
      assert.ok(
        settledInOneRender(bodies),
        `${bodies.length} simultaneous requests from ${VANTAGES.length} regions were answered by ${new Set(rendersOf(bodies)).size} renders, so Origin Shield did not collapse them into one origin request`,
      );
    },
  },
];
