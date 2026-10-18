import { lookup as dnsLookup, type LookupAddress } from "node:dns";
import { isIP } from "node:net";
import {
  type CompiledImageConfig,
  isAllowedRemote,
  UPSTREAM_TIMEOUT_MS,
} from "@framework/next-protocol/image";
import { Agent, request } from "undici";
import { isReachableAddress } from "./addresses.mjs";
import { type ImageError, upstreamFailure, upstreamTimeout } from "./errors.mjs";
import { readCapped, TooLargeError } from "./stream.mjs";

const REDIRECT_STATUSES = new Set([301, 302, 303, 307, 308]);

const TIMEOUT_CODES = new Set([
  "UND_ERR_CONNECT_TIMEOUT",
  "UND_ERR_HEADERS_TIMEOUT",
  "UND_ERR_BODY_TIMEOUT",
]);

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

export function guardedLookup(deps: UpstreamDeps, allowLocalIP = false): typeof dnsLookup {
  const resolve = deps.lookup ?? dnsLookup;
  const isReachable = deps.isReachable ?? isReachableAddress;

  return ((hostname: string, options: any, callback: LookupCallback) => {
    const wantsAll = typeof options === "object" && options !== null && options.all === true;
    const base =
      typeof options === "object" && options !== null
        ? options
        : typeof options === "number"
          ? { family: options }
          : {};
    const opts = { ...base, all: true };
    resolve(hostname, opts, (err: NodeJS.ErrnoException | null, addresses: unknown) => {
      if (err) return callback(err, "", 0);
      const list = (addresses as LookupAddress[]) ?? [];
      const reachable = allowLocalIP ? list : list.filter((entry) => isReachable(entry.address));
      if (reachable.length === 0) {
        return callback(new BlockedAddressError(hostname), "", 0);
      }
      if (wantsAll) return callback(null, reachable);
      const first = reachable[0]!;
      callback(null, first.address, first.family);
    });
  }) as typeof dnsLookup;
}

function agentFor(deps: UpstreamDeps, allowLocalIP: boolean): Agent {
  return new Agent({
    connect: { lookup: guardedLookup(deps, allowLocalIP) },
    connectTimeout: UPSTREAM_TIMEOUT_MS,
    headersTimeout: UPSTREAM_TIMEOUT_MS,
    bodyTimeout: UPSTREAM_TIMEOUT_MS,
  });
}

let guardedAgent: Agent | undefined;

let localAgent: Agent | undefined;

export async function fetchUpstream(
  href: string,
  config: CompiledImageConfig,
  deps: UpstreamDeps = {},
): Promise<UpstreamImage> {
  const injected = deps.lookup !== undefined || deps.isReachable !== undefined;
  const allowLocalIP = config.dangerouslyAllowLocalIP;
  const agent = injected
    ? agentFor(deps, allowLocalIP)
    : allowLocalIP
      ? (localAgent ??= agentFor(deps, true))
      : (guardedAgent ??= agentFor(deps, false));
  try {
    return await follow(href, config, agent, deps.isReachable ?? isReachableAddress);
  } finally {
    if (injected) await agent.close().catch(() => {});
  }
}

function assertReachableLiteral(url: URL, isReachable: (address: string) => boolean): void {
  const hostname = url.hostname;
  const bare =
    hostname.startsWith("[") && hostname.endsWith("]") ? hostname.slice(1, -1) : hostname;
  if (isIP(bare) !== 0 && !isReachable(hostname)) {
    throw upstreamFailure(`unreachable address ${hostname}`);
  }
}

async function follow(
  href: string,
  config: CompiledImageConfig,
  agent: Agent,
  isReachable: (address: string) => boolean,
): Promise<UpstreamImage> {
  const signal = AbortSignal.timeout(UPSTREAM_TIMEOUT_MS);
  const limit = config.maximumResponseBody;

  let current: URL;
  try {
    current = new URL(href);
  } catch (error) {
    throw upstreamFailure(error);
  }
  for (let hop = 0; ; hop++) {
    if (!config.dangerouslyAllowLocalIP) {
      assertReachableLiteral(current, isReachable);
    }
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
      throw failedRead(error);
    }

    const location = header(response.headers.location);
    if (REDIRECT_STATUSES.has(response.statusCode) && location) {
      await response.body.dump().catch(() => {});
      if (hop >= config.maximumRedirects) {
        throw upstreamFailure(`too many redirects for ${href}`, 508);
      }
      current = resolveHop(location, current, config);
      continue;
    }

    if (response.statusCode < 200 || response.statusCode > 299) {
      await response.body.dump().catch(() => {});
      throw upstreamFailure(
        `upstream status ${response.statusCode} for ${current}`,
        answeredStatus(response.statusCode),
      );
    }

    const declared = Number(header(response.headers["content-length"]));
    if (Number.isFinite(declared) && declared > limit) {
      await response.body.dump().catch(() => {});
      throw upstreamFailure(`declared length ${declared} over ${limit}`, 413);
    }

    try {
      const bytes = await readCapped(response.body, limit);
      return {
        bytes,
        cacheControl: header(response.headers["cache-control"]),
        etag: header(response.headers.etag),
      };
    } catch (error) {
      response.body.destroy();
      throw failedRead(error);
    }
  }
}

function failedRead(error: unknown): ImageError {
  if (error instanceof TooLargeError) return upstreamFailure(error, 413);
  if (isTimeout(error)) return upstreamTimeout(error);
  return upstreamFailure(error);
}

function isTimeout(error: unknown): boolean {
  if (!(error instanceof Error)) return false;
  const code = (error as NodeJS.ErrnoException).code;
  return error.name === "TimeoutError" || (code !== undefined && TIMEOUT_CODES.has(code));
}

function answeredStatus(upstream: number): number {
  return upstream >= 400 && upstream <= 599 ? upstream : 500;
}

function resolveHop(location: string, from: URL, config: CompiledImageConfig): URL {
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
  return next;
}

function header(value: string | string[] | undefined): string | null {
  if (value === undefined) return null;
  return Array.isArray(value) ? (value[0] ?? null) : value;
}
