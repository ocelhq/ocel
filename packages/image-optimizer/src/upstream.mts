import { lookup as dnsLookup, type LookupAddress } from "node:dns";
import { Agent, request } from "undici";
import { isReachableAddress } from "./addresses.mjs";
import type { CompiledImageConfig } from "./contract.mjs";
import { upstreamFailure } from "./errors.mjs";
import { isAllowedRemote } from "./patterns.mjs";
import { readCapped } from "./stream.mjs";

const UPSTREAM_TIMEOUT_MS = 7_000;

const MAX_REDIRECTS = 3;

const REDIRECT_STATUSES = new Set([301, 302, 303, 307, 308]);

export interface UpstreamImage {
  bytes: Uint8Array;
  cacheControl: string | null;
  etag: string | null;
}

type LookupCallback = (
  err: NodeJS.ErrnoException | null,
  address: string | LookupAddress[],
  family?: number,
) => void;

export interface UpstreamDeps {
  lookup?: typeof dnsLookup;
  isReachable?: (address: string) => boolean;
}

class BlockedAddressError extends Error {
  constructor(hostname: string) {
    super(`no reachable address for ${hostname}`);
    this.name = "BlockedAddressError";
  }
}

export function guardedLookup(deps: UpstreamDeps): typeof dnsLookup {
  const resolve = deps.lookup ?? dnsLookup;
  const isReachable = deps.isReachable ?? isReachableAddress;

  return ((hostname: string, options: any, callback: LookupCallback) => {
    const wantsAll = typeof options === "object" && options !== null && options.all === true;
    const opts = { ...(typeof options === "object" && options !== null ? options : {}), all: true };
    resolve(hostname, opts, (err: NodeJS.ErrnoException | null, addresses: unknown) => {
      if (err) return callback(err, "", 0);
      const list = (addresses as LookupAddress[]) ?? [];
      const reachable = list.filter((entry) => isReachable(entry.address));
      if (reachable.length === 0) {
        return callback(new BlockedAddressError(hostname), "", 0);
      }
      if (wantsAll) return callback(null, reachable);
      const first = reachable[0]!;
      callback(null, first.address, first.family);
    });
  }) as typeof dnsLookup;
}

function agentFor(deps: UpstreamDeps): Agent {
  return new Agent({
    connect: { lookup: guardedLookup(deps) },
    connectTimeout: UPSTREAM_TIMEOUT_MS,
    headersTimeout: UPSTREAM_TIMEOUT_MS,
    bodyTimeout: UPSTREAM_TIMEOUT_MS,
  });
}

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
          "accept-encoding": "identity",
        },
      });
    } catch (error) {
      throw upstreamFailure(error);
    }

    const location = header(response.headers["location"]);
    if (REDIRECT_STATUSES.has(response.statusCode) && location) {
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
