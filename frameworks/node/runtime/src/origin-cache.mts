import { AsyncLocalStorage } from "node:async_hooks";
import type http from "node:http";
import { buildPathCacheTag, storedCacheTags } from "@platform/edge-contract/cache-tags";
import {
  edgeAsksToCacheVar,
  edgeCacheHeader,
  edgeCacheTagHeader,
  releaseTokenVar,
} from "@platform/edge-contract/edge-cache";
import {
  type PurgeEntry,
  type PurgeMode,
  purgeHeader,
  purgeHeaderFor,
} from "@platform/edge-contract/purge";
import { formatCacheControl } from "@platform/edge-contract/routes";
import type { OcelContext } from "./host.mjs";
import type { RoutedRequest } from "./origin-routes.mjs";
import { findTagRecordWriter, type WriteTagRecords } from "./tag-records.mjs";

const cacheHostKey = Symbol.for("ocel.cache.host");

const releasePattern = /^r[0-9a-f]{8}$/;

const cacheTagHeader = "cache-tag";

export interface OriginCache {
  release: string;
  edgeAsks: boolean;
}

export interface RequestCache {
  tags: Set<string>;
  revalidations: Map<string, PurgeMode>;
  raised: Map<string, PurgeEntry>;
  writing: boolean;
  res: http.ServerResponse;
  ocel: OcelContext;
}

export function readOriginCache(env: NodeJS.ProcessEnv): OriginCache | null {
  const release = env[releaseTokenVar];
  if (!release) return null;
  if (!releasePattern.test(release)) {
    throw new Error(`ocel: ${releaseTokenVar} is ${release}, which is not a release token`);
  }
  return { release, edgeAsks: Boolean(env[edgeAsksToCacheVar]) };
}

const requests = new AsyncLocalStorage<RequestCache>();

let warnedUnreachable = false;

function readRequestCache(call: string): RequestCache {
  const store = requests.getStore();
  if (!store) {
    throw new Error(`ocel: ${call} was called outside a request, so no response can carry it`);
  }
  return store;
}

function writeRaisedAfterResponse(write: WriteTagRecords, store: RequestCache): void {
  store.writing = true;
  const settled = new Promise<void>((resolve) => store.res.once("close", resolve)).then(
    async () => {
      const entries = [...store.raised.values()];
      try {
        await write(entries);
      } catch (error) {
        console.error(
          `ocel: the revalidation of ${entries.map(({ tag }) => tag).join(", ")} was not written for the edge to clear: ${error instanceof Error ? error.message : String(error)}`,
        );
      }
    },
  );
  store.ocel.waitUntil(settled);
}

export function installCacheHost(cache: OriginCache): void {
  const revalidate = (tag: string, mode: PurgeMode, call: string): void => {
    const store = readRequestCache(call);
    if (storedCacheTags(cache.release, [tag]).unstorable.length > 0) {
      console.warn(`ocel: ${call} names a tag no edge can store, so it clears nothing`);
      return;
    }
    const write = findTagRecordWriter();
    if (!cache.edgeAsks && !write && !warnedUnreachable) {
      warnedUnreachable = true;
      console.warn(
        `ocel: ${call} reaches no edge, because the edge in front of this app takes no purge from it; what ${tag} names lapses with its cache lifetime`,
      );
    }
    if (store.revalidations.get(tag) !== "purge") store.revalidations.set(tag, mode);
    if (store.raised.get(tag)?.mode !== "purge") {
      store.raised.set(tag, { tag, mode, raisedAt: Date.now() });
    }
    if (write && !store.writing) writeRaisedAfterResponse(write, store);
  };
  (globalThis as Record<symbol, unknown>)[cacheHostKey] = {
    tag: (tags: readonly string[]) => {
      const store = readRequestCache("cacheTag");
      for (const tag of tags) store.tags.add(tag);
    },
    revalidateTag: (tag: string, mode: PurgeMode) => revalidate(tag, mode, `revalidateTag(${tag})`),
    revalidatePath: (path: string, mode: PurgeMode) =>
      revalidate(buildPathCacheTag(path), mode, `revalidatePath(${path})`),
  };
}

export function removeCacheHost(): void {
  delete (globalThis as Record<symbol, unknown>)[cacheHostKey];
}

export function openRequestCache(res: http.ServerResponse, ocel: OcelContext): RequestCache {
  return {
    tags: new Set(),
    revalidations: new Map(),
    raised: new Map(),
    writing: false,
    res,
    ocel,
  };
}

export function runWithRequestCache<T>(store: RequestCache, run: () => T): T {
  return requests.run(store, run);
}

export function watchRequestCache(
  cache: OriginCache,
  res: http.ServerResponse,
  store: RequestCache,
): void {
  res.once("close", () => {
    if (store.revalidations.size > 0 && cache.edgeAsks) warnLostRevalidations(store);
  });
}

function warnLostRevalidations(store: RequestCache): void {
  console.warn(
    `ocel: ${[...store.revalidations.keys()].join(", ")} was revalidated before the response was sent, so it reaches no edge. Revalidate before the response begins`,
  );
}

export function prepareCachedResponse(
  cache: OriginCache,
  req: http.IncomingMessage,
  res: http.ServerResponse,
  store: RequestCache,
  routed: RoutedRequest,
): void {
  announce(res, cache, store);
  if (routed.bypass === "bypass") {
    res.setHeader("cache-control", "private, no-store");
    res.removeHeader(cacheTagHeader);
    res.removeHeader(edgeCacheTagHeader);
    return;
  }
  shape(req, res, cache, store, routed);
}

function announce(res: http.ServerResponse, cache: OriginCache, store: RequestCache): void {
  if (!cache.edgeAsks || store.revalidations.size === 0) return;
  const raisedAt = Date.now();
  const entries: PurgeEntry[] = [...store.revalidations].map(([tag, mode]) => ({
    tag,
    mode,
    raisedAt,
  }));
  const announced = purgeHeaderFor(cache.release, entries);
  if (announced !== undefined) res.setHeader(purgeHeader, announced);
  store.revalidations.clear();
}

function shape(
  req: http.IncomingMessage,
  res: http.ServerResponse,
  cache: OriginCache,
  store: RequestCache,
  routed: RoutedRequest,
): void {
  const asked = req.headers[edgeCacheHeader] === "1";
  if (cache.edgeAsks && !asked) return;
  if ((req.method !== "GET" && req.method !== "HEAD") || res.statusCode !== 200) return;
  if (res.getHeader("set-cookie") !== undefined) return;

  const declared = String(res.getHeader("cache-control") ?? "");
  const declaredNames = listDirectiveNames(declared);
  if (["private", "no-store", "no-cache"].some((name) => declaredNames.includes(name))) return;

  if (!declaredNames.includes("s-maxage")) {
    if (req.headers.authorization !== undefined || !routed.cacheRule) return;
    const ruled = formatCacheControl(routed.cacheRule);
    res.setHeader("cache-control", declared === "" ? ruled : `${declared}, ${ruled}`);
  }

  const { tags, unstorable } = storedCacheTags(cache.release, [
    ...(routed.cacheRule?.tags ?? []),
    ...store.tags,
    buildPathCacheTag(routed.pathname),
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

function listDirectiveNames(declared: string): string[] {
  return declared
    .toLowerCase()
    .split(",")
    .map((directive) => directive.trim().split("=")[0]!);
}
