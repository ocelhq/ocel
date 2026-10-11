import { AsyncLocalStorage } from "node:async_hooks";
import type http from "node:http";
import {
  cacheControlOfRule,
  findCacheRule,
  readCacheRules,
} from "@platform/edge-contract/cache-rules";
import { buildPathCacheTag, storedCacheTags } from "@platform/edge-contract/cache-tags";
import { edgeCacheHeader, edgeCacheTagHeader } from "@platform/edge-contract/edge-cache";
import type { CacheRule } from "@platform/edge-contract/hosting";
import {
  type PurgeEntry,
  type PurgeMode,
  purgeHeader,
  purgeHeaderFor,
} from "@platform/edge-contract/purge";
import { setRequestScope } from "./host.mjs";

const cacheHostKey = Symbol.for("ocel.cache.host");

const releaseTokenVar = "OCEL_RELEASE_TOKEN";
const edgeAsksVar = "OCEL_EDGE_ASKS_TO_CACHE";

const releasePattern = /^r[0-9a-f]{8}$/;

const cacheTagHeader = "cache-tag";

interface OriginCache {
  rules: CacheRule[];
  release: string;
  edgeAsks: boolean;
}

interface RequestCache {
  tags: Set<string>;
  raises: Map<string, PurgeMode>;
}

function readOriginCache(env: NodeJS.ProcessEnv): OriginCache | null {
  const release = env[releaseTokenVar];
  if (!release) return null;
  if (!releasePattern.test(release)) {
    throw new Error(`ocel: ${releaseTokenVar} is ${release}, which is not a release token`);
  }
  return {
    rules: readCacheRules(env.OCEL_CACHE_RULES),
    release,
    edgeAsks: Boolean(env[edgeAsksVar]),
  };
}

const requests = new AsyncLocalStorage<RequestCache>();

let warnedUnreachable = false;

function currentRequest(call: string): RequestCache {
  const store = requests.getStore();
  if (!store) {
    throw new Error(`ocel: ${call} was called outside a request, so no response can carry it`);
  }
  return store;
}

function installOriginCache(cache: OriginCache): void {
  const raise = (tag: string, mode: PurgeMode, call: string): void => {
    const store = currentRequest(call);
    if (storedCacheTags(cache.release, [tag]).unstorable.length > 0) {
      console.warn(`ocel: ${call} names a tag no edge can store, so it clears nothing`);
      return;
    }
    if (!cache.edgeAsks && !warnedUnreachable) {
      warnedUnreachable = true;
      console.warn(
        `ocel: ${call} cannot reach the edge in front of this app, because only the Cloudflare edge purges for a node app; ${tag} lapses with the cache lifetime of what it names`,
      );
    }
    if (store.raises.get(tag) !== "purge") store.raises.set(tag, mode);
  };
  (globalThis as Record<symbol, unknown>)[cacheHostKey] = {
    tag: (tags: readonly string[]) => {
      const store = currentRequest("cacheTag");
      for (const tag of tags) store.tags.add(tag);
    },
    revalidateTag: (tag: string, mode: "expire" | "stale") =>
      raise(tag, mode === "expire" ? "purge" : "invalidate", `revalidateTag(${tag})`),
    revalidatePath: (path: string) =>
      raise(buildPathCacheTag(path), "purge", `revalidatePath(${path})`),
  };
}

export function useOriginCache(env: NodeJS.ProcessEnv): void {
  const cache = readOriginCache(env);
  if (!cache) {
    setRequestScope(undefined);
    delete (globalThis as Record<symbol, unknown>)[cacheHostKey];
    return;
  }
  installOriginCache(cache);
  setRequestScope((req, res, run) => runInRequestCache(cache, req, res, run));
}

function runInRequestCache<T>(
  cache: OriginCache,
  req: http.IncomingMessage,
  res: http.ServerResponse,
  run: () => T,
): T {
  const store: RequestCache = { tags: new Set(), raises: new Map() };
  const writeHead = res.writeHead;
  res.writeHead = function (this: http.ServerResponse, ...args: any[]) {
    if (this.headersSent) return (writeHead as any).apply(this, args);
    const [status, ...rest] = args;
    if (typeof status === "number") this.statusCode = status;
    const message = typeof rest[0] === "string" ? [rest.shift()] : [];
    declareHeaders(this, rest[0]);
    announce(this, cache, store);
    shape(req, this, cache, store);
    return (writeHead as any).apply(this, [status, ...message]);
  } as typeof res.writeHead;
  res.once("close", () => {
    if (store.raises.size > 0 && cache.edgeAsks) warnLostRaises(store);
  });
  return requests.run(store, run);
}

function declareHeaders(res: http.ServerResponse, declared: unknown): void {
  if (Array.isArray(declared)) {
    const pairs = Array.isArray(declared[0]) ? declared : chunked(declared);
    for (const [name, value] of pairs) res.setHeader(name, value);
  } else if (declared !== null && typeof declared === "object") {
    for (const [name, value] of Object.entries(declared)) {
      if (value !== undefined) res.setHeader(name, value as string | string[]);
    }
  }
}

function chunked(flat: unknown[]): [string, string][] {
  const pairs: [string, string][] = [];
  for (let i = 0; i + 1 < flat.length; i += 2) pairs.push([String(flat[i]), String(flat[i + 1])]);
  return pairs;
}

function warnLostRaises(store: RequestCache): void {
  console.warn(
    `ocel: ${[...store.raises.keys()].join(", ")} was revalidated before the response was sent, so it reaches no edge. Revalidate before the response begins`,
  );
}

function announce(res: http.ServerResponse, cache: OriginCache, store: RequestCache): void {
  if (!cache.edgeAsks || store.raises.size === 0) return;
  const raisedAt = Date.now();
  const entries: PurgeEntry[] = [...store.raises].map(([tag, mode]) => ({ tag, mode, raisedAt }));
  const announced = purgeHeaderFor(cache.release, entries);
  if (announced !== undefined) res.setHeader(purgeHeader, announced);
  store.raises.clear();
}

function shape(
  req: http.IncomingMessage,
  res: http.ServerResponse,
  cache: OriginCache,
  store: RequestCache,
): void {
  const asked = req.headers[edgeCacheHeader] === "1";
  if (cache.edgeAsks && !asked) return;
  if ((req.method !== "GET" && req.method !== "HEAD") || res.statusCode !== 200) return;
  if (res.getHeader("set-cookie") !== undefined) return;

  const declared = String(res.getHeader("cache-control") ?? "");
  const declaredNames = directives(declared);
  if (["private", "no-store", "no-cache"].some((name) => declaredNames.includes(name))) return;

  const pathname = (req.url ?? "/").split("?")[0]!;
  if (!declaredNames.includes("s-maxage")) {
    if (carriesCredentials(req)) return;
    const rule = findCacheRule(cache.rules, pathname);
    if (!rule) return;
    const ruled = cacheControlOfRule(rule);
    res.setHeader("cache-control", declared === "" ? ruled : `${declared}, ${ruled}`);
  }

  const { tags, unstorable } = storedCacheTags(cache.release, [
    ...store.tags,
    buildPathCacheTag(pathname),
  ]);
  if (unstorable.length > 0) {
    console.warn(
      `ocel: ${req.url} has cache tags the edge cannot store, so revalidating ${unstorable.join(", ")} will not reach it`,
    );
  }
  const header = asked ? edgeCacheTagHeader : cacheTagHeader;
  const existing = String(res.getHeader(header) ?? "")
    .split(",")
    .filter((tag) => tag !== "");
  res.setHeader(header, [...new Set([...existing, ...tags])].join(","));
}

function carriesCredentials(req: http.IncomingMessage): boolean {
  return req.headers.cookie !== undefined || req.headers.authorization !== undefined;
}

function directives(declared: string): string[] {
  return declared
    .toLowerCase()
    .split(",")
    .map((directive) => directive.trim().split("=")[0]!);
}
