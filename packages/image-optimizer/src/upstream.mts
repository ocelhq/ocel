import { lookup as dnsLookup, type LookupAddress } from "node:dns";
import { Agent, request } from "undici";
import { isReachableAddress } from "./addresses.mjs";
import type { CompiledImageConfig } from "./contract.mjs";
import { upstreamFailure } from "./errors.mjs";
import { isAllowedRemote } from "./patterns.mjs";
import { readCapped } from "./stream.mjs";

// Fetching a remote source image from inside the customer's AWS account.
//
// This is the SSRF surface. Everything below exists because the URL is named by
// whoever can spell a /_next/image request, and the socket it opens starts
// inside a VPC-adjacent network position holding an IAM role.
//
// The config's `dangerouslyAllowLocalIP` is carried through the manifest and is
// deliberately not honoured here. Under `next start` that flag disables a check
// on a server the app owns alone. This function is account-global: one app
// setting it would open the substrate's own network — including the instance
// metadata service, and with it this function's role over every tenant's
// assets — to every app sharing the account. The design's IP policy is stated
// as default-deny with no exception, and this is what that means in a config
// that still carries the knob.

// The whole fetch, redirects and body included, gets this long. A byte ceiling
// does nothing against an origin that sends one byte a minute, and Lambda bills
// for the wait; the wall clock is the only thing that ends that request.
const UPSTREAM_TIMEOUT_MS = 7_000;

// Next's cap, and the design's. Re-validating the allowlist on every hop makes
// a longer chain progressively less useful to an attacker anyway.
const MAX_REDIRECTS = 3;

const REDIRECT_STATUSES = new Set([301, 302, 303, 307, 308]);

// What survives from the upstream response. The upstream's Content-Type is
// deliberately not among them: nothing downstream is allowed to consult it (see
// sniff.mts), so it is not carried where something could.
export interface UpstreamImage {
  bytes: Uint8Array;
  // Relayed verbatim as this function's own Cache-Control. The edge never talks
  // to this server, so this header is the only path by which its freshness
  // reaches the tier that caches on it.
  cacheControl: string | null;
  etag: string | null;
}

type LookupCallback = (
  err: NodeJS.ErrnoException | null,
  address: string | LookupAddress[],
  family?: number,
) => void;

export interface UpstreamDeps {
  // Both are injected only so the guarantees below can be tested: a test needs
  // to serve from loopback (which the real policy denies) and to make a second
  // resolution differ from the first. Production uses the defaults.
  lookup?: typeof dnsLookup;
  isReachable?: (address: string) => boolean;
}

class BlockedAddressError extends Error {
  constructor(hostname: string) {
    super(`no reachable address for ${hostname}`);
    this.name = "BlockedAddressError";
  }
}

// Resolution, validation and connection as one step.
//
// The alternative — resolve, check the addresses, then fetch by hostname — is
// a TOCTOU with a DNS server the attacker owns: the check sees a public
// address, the connection re-resolves and gets 169.254.169.254. Next does it
// that way. Here the socket is opened to an address this function has already
// approved, because this *is* the resolution the socket uses; there is no
// window between them to rebind in.
export function guardedLookup(deps: UpstreamDeps): typeof dnsLookup {
  const resolve = deps.lookup ?? dnsLookup;
  const isReachable = deps.isReachable ?? isReachableAddress;

  return ((hostname: string, options: any, callback: LookupCallback) => {
    const wantsAll = typeof options === "object" && options !== null && options.all === true;
    // Resolved with all:true whatever the caller asked for, so the filter sees
    // every address the name has and not just the first. Node 20+ turns on
    // autoSelectFamily, which forces all:true here anyway — but a caller that
    // asked for a single address still gets a single address back, because
    // handing undici an array it did not ask for is how this pattern is most
    // often broken.
    const opts = { ...(typeof options === "object" && options !== null ? options : {}), all: true };
    resolve(hostname, opts, (err: NodeJS.ErrnoException | null, addresses: unknown) => {
      if (err) return callback(err, "", 0);
      const list = (addresses as LookupAddress[]) ?? [];
      const reachable = list.filter((entry) => isReachable(entry.address));
      // Filtered, not all-or-nothing: a name with one public and one private
      // record is served from the public one. What must never happen is the
      // private one surviving into the list undici picks from.
      if (reachable.length === 0) {
        return callback(new BlockedAddressError(hostname), "", 0);
      }
      if (wantsAll) return callback(null, reachable);
      const first = reachable[0]!;
      callback(null, first.address, first.family);
    });
  }) as typeof dnsLookup;
}

// Redirect following is off, which in undici 8 means the redirect interceptor is
// simply not installed — the `maxRedirections: 0` of earlier versions is now the
// only state a bare Agent can be in, and there is no option left to pass. Every
// hop is therefore taken by hand below, because a followed redirect is a fetch
// this function never got to check the allowlist for.
function agentFor(deps: UpstreamDeps): Agent {
  return new Agent({
    connect: { lookup: guardedLookup(deps) },
    connectTimeout: UPSTREAM_TIMEOUT_MS,
    headersTimeout: UPSTREAM_TIMEOUT_MS,
    bodyTimeout: UPSTREAM_TIMEOUT_MS,
  });
}

// One pooled agent for the default policy, so a warm container reuses
// connections. A test-supplied policy gets its own and closes it.
let defaultAgent: Agent | undefined;

export async function fetchUpstream(
  href: string,
  config: CompiledImageConfig,
  deps: UpstreamDeps = {},
): Promise<UpstreamImage> {
  const injected = deps.lookup !== undefined || deps.isReachable !== undefined;
  const agent = injected ? agentFor(deps) : (defaultAgent ??= agentFor(deps));
  try {
    return await follow(href, config, agent);
  } finally {
    if (injected) await agent.close().catch(() => {});
  }
}

async function follow(
  href: string,
  config: CompiledImageConfig,
  agent: Agent,
): Promise<UpstreamImage> {
  // One deadline for the whole chain. Per-hop timeouts would let three
  // redirects buy three times the budget.
  const signal = AbortSignal.timeout(UPSTREAM_TIMEOUT_MS);
  const limit = config.maximumResponseBody;
  const maxHops = Math.min(config.maximumRedirects ?? MAX_REDIRECTS, MAX_REDIRECTS);

  let current = href;
  for (let hop = 0; ; hop++) {
    let response;
    try {
      response = await request(current, {
        dispatcher: agent,
        method: "GET",
        signal,
        headers: {
          accept: "image/*",
          // Removes the decompression-bomb class by construction and makes
          // Content-Length mean what it says, so the cap below can be checked
          // before a byte of body is read.
          "accept-encoding": "identity",
        },
      });
    } catch (error) {
      throw upstreamFailure(error);
    }

    const location = header(response.headers["location"]);
    if (REDIRECT_STATUSES.has(response.statusCode) && location) {
      // Drained rather than abandoned: an undrained body holds the socket out
      // of the pool until the agent times it out.
      await response.body.dump().catch(() => {});
      if (hop >= maxHops) throw upstreamFailure(`too many redirects for ${href}`);
      const next = resolveHop(location, current, config);
      current = next;
      continue;
    }

    if (response.statusCode !== 200) {
      await response.body.dump().catch(() => {});
      throw upstreamFailure(`upstream status ${response.statusCode} for ${current}`);
    }

    const declared = Number(header(response.headers["content-length"]));
    if (Number.isFinite(declared) && declared > limit) {
      await response.body.dump().catch(() => {});
      throw upstreamFailure(`declared length ${declared} over ${limit}`);
    }

    try {
      const bytes = await readCapped(response.body, limit);
      return {
        bytes,
        cacheControl: header(response.headers["cache-control"]),
        etag: header(response.headers["etag"]),
      };
    } catch (error) {
      response.body.destroy();
      throw upstreamFailure(error);
    }
  }
}

// Divergence 1 from Next, deliberate and registered. Next states that redirects
// "do not need to satisfy remotePatterns"; on an optimizer shared by every app
// in an account, that turns an open redirect on any one tenant's allowlisted
// CDN into a fetch primitive aimed at everyone's. The IP policy re-runs too, at
// connect time, because it lives in the agent this hop reuses.
function resolveHop(
  location: string,
  from: string,
  config: CompiledImageConfig,
): string {
  let next: URL;
  try {
    next = new URL(location, from);
  } catch (error) {
    throw upstreamFailure(error);
  }
  if (next.protocol !== "http:" && next.protocol !== "https:") {
    throw upstreamFailure(`redirect to ${next.protocol} from ${from}`);
  }
  if (!isAllowedRemote(config, next)) {
    throw upstreamFailure(`redirect to disallowed ${next.href} from ${from}`);
  }
  return next.href;
}

function header(value: string | string[] | undefined): string | null {
  if (value === undefined) return null;
  return Array.isArray(value) ? (value[0] ?? null) : value;
}
