import assert from "node:assert/strict";
import type { Phase } from "../matrix/types";
import type { Check } from "./context";
import { askOverPlainHTTP, askOverTLS, type Outcome, readOriginAddress } from "./originShield";

const TUNNEL_DOMAIN = ".cfargotunnel.com";
const REDIRECTS = [301, 302, 307, 308];

export function assertTunnelAddress(address: string, hostname: string): void {
  assert.ok(
    address.endsWith(TUNNEL_DOMAIN),
    `${hostname}'s proxied record names ${address}, want a Cloudflare Tunnel (<uuid>${TUNNEL_DOMAIN})`,
  );
}

export function assertNotServed(outcome: Outcome, where: string): void {
  if (outcome.kind === "refused" || outcome.kind === "unreachable") return;
  if (outcome.kind === "answered" && outcome.status !== undefined && outcome.status >= 400) return;
  const said =
    outcome.kind === "answered"
      ? `answered ${outcome.status ?? "with no status line"}:\n${outcome.said.slice(0, 300)}`
      : `connected and then settled nothing: ${outcome.reason}`;
  throw new assert.AssertionError({
    message: `${where} ${said}\nwant a refusal or an error status for a hostname only the tunnel reaches`,
  });
}

export function assertRedirectedOffPlainHTTP(
  status: number,
  location: string | null,
  hostname: string,
): void {
  const want = `https://${hostname}/`;
  assert.ok(
    REDIRECTS.includes(status) && location === want,
    `http://${hostname}/ answered ${status} to ${location ?? "nowhere"}; want a redirect to ${want}`,
  );
}

export function assertRecordKeptAcrossAPromote(
  before: string,
  after: string,
  hostname: string,
  phase: Phase,
): void {
  assert.equal(
    after,
    before,
    `after the ${phase}, ${hostname}'s proxied record names ${after}, want ${before}: a promote flips the release on the box, and the tunnel the record names stays`,
  );
}

function token(): string {
  const read = process.env.CLOUDFLARE_API_TOKEN?.trim();
  assert.ok(
    read,
    "CLOUDFLARE_API_TOKEN is unset, and the record a tunnel is reached by is read with it",
  );
  return read;
}

function box(): string {
  const read = process.env.OCEL_VPS_HOST?.trim();
  assert.ok(read, "OCEL_VPS_HOST is unset, and the box a tunnel reaches is asked directly at it");
  return read;
}

const reachedThroughATunnel: Check = {
  title: "the hostname's proxied record names a Cloudflare Tunnel",
  run: async (ctx) => {
    const hostname = new URL(ctx.baseUrl).hostname;
    assertTunnelAddress(await readOriginAddress(hostname, token()), hostname);
  },
};

const refusedAroundTheTunnel: Check = {
  title: "the box does not serve the hostname to a request that reaches its 443 or 80 directly",
  run: async (ctx) => {
    const hostname = new URL(ctx.baseUrl).hostname;
    const address = box();
    assertNotServed(
      await askOverTLS({ address, port: 443, hostname }),
      `${address}:443 for ${hostname}`,
    );
    assertNotServed(
      await askOverPlainHTTP({ address, port: 80, hostname }),
      `${address}:80 for ${hostname}`,
    );
  },
};

const redirectedOverPlainHTTP: Check = {
  title: "a visitor over plain http is redirected to https",
  run: async (ctx) => {
    const hostname = new URL(ctx.baseUrl).hostname;
    const answer = await ctx.fetch(`http://${hostname}/`, { redirect: "manual" });
    await answer.body?.cancel();
    assertRedirectedOffPlainHTTP(answer.status, answer.headers.get("location"), hostname);
  },
};

const keptAcrossAPromote: Check = {
  title: "a promote leaves the hostname's proxied record naming the same tunnel",
  run: async (ctx) => {
    const hostname = new URL(ctx.baseUrl).hostname;
    const address = await readOriginAddress(hostname, token());
    const note = `tunnel record ${ctx.app}`;
    const before = ctx.notes.get(note);
    if (ctx.phase !== "verify" && before !== undefined) {
      assertRecordKeptAcrossAPromote(before, address, hostname, ctx.phase);
    }
    ctx.notes.set(note, address);
  },
};

export const TUNNELED_ORIGIN_CHECKS: Check[] = [
  reachedThroughATunnel,
  keptAcrossAPromote,
  refusedAroundTheTunnel,
  redirectedOverPlainHTTP,
];
