import { isolateId } from "./isolate";
import {
  claim,
  controlPath,
  parseScope,
  racePath,
  record,
  scopedKey,
} from "./race";

// Every judgement is left to the runner. This worker reports what it saw and
// nothing more — it never compares two isolates' clocks, because Date.now() in a
// Worker advances only on I/O and differencing it across isolates would measure
// the runtime rather than the cache.

interface Sentinel {
  run: string;
  writer: string;
  ttlSeconds: number;
}

const cacheKey = (url: URL, run: string) =>
  new Request(new URL(`/__cache-probe/${encodeURIComponent(run)}`, url.origin).toString());

function identity(request: Request, url: URL) {
  return {
    isolate: isolateId(),
    colo: (request.cf?.colo as string | undefined) ?? null,
    host: url.host,
  };
}

export default {
  async fetch(request: Request): Promise<Response> {
    const url = new URL(request.url);
    const base = identity(request, url);
    const run = url.searchParams.get("run");

    if (url.pathname === "/identity") return Response.json(base);

    if (url.pathname === "/entry") {
      if (!run) return new Response("run is required", { status: 400 });
      const cache = caches.default;
      const key = cacheKey(url, run);

      if (request.method === "PUT") {
        const ttlSeconds = Number(url.searchParams.get("ttl") ?? "10");
        if (!Number.isFinite(ttlSeconds) || ttlSeconds <= 0) {
          return new Response("ttl must be a positive number", { status: 400 });
        }
        const sentinel: Sentinel = { run, writer: base.isolate, ttlSeconds };
        await cache.put(
          key,
          Response.json(sentinel, {
            headers: { "cache-control": `max-age=${ttlSeconds}` },
          }),
        );
        // Positive control: read the entry back from the isolate that just wrote
        // it. Without this, a later run-wide miss cannot be told apart from a
        // cache that never stored anything at all.
        const verified = await cache.match(key);
        return Response.json({
          ...base,
          sentinel,
          verified: verified !== undefined,
          verifiedCacheControl: verified?.headers.get("cache-control") ?? null,
        });
      }

      if (request.method === "GET") {
        const hit = await cache.match(key);
        const sentinel = hit ? ((await hit.json()) as Sentinel) : null;
        return Response.json({
          ...base,
          hit: sentinel !== null,
          writer: sentinel?.writer ?? null,
          requestedTtlSeconds: sentinel?.ttlSeconds ?? null,
          // Cloudflare's own view of how long it has held the entry, and of the
          // freshness it decided to store it under. Independent of any clock
          // this probe reads and of the runner's polling luck.
          age: hit?.headers.get("age") ?? null,
          cacheControl: hit?.headers.get("cache-control") ?? null,
        });
      }

      return new Response("Method Not Allowed", { status: 405 });
    }

    // Two racers must never be answered by one body. The zone's own edge cache
    // sits in front of this worker, so every racing response is no-store and
    // every racer's URL is unique by &seq — either alone would be enough, and
    // a manufactured duplicate claim is not a mistake worth being clever about.
    const racing = (body: unknown) =>
      Response.json(body, { headers: { "cache-control": "no-store" } });

    if (url.pathname === "/race") {
      if (request.method !== "POST") return new Response("Method Not Allowed", { status: 405 });
      const key = url.searchParams.get("key");
      if (!key) return new Response("key is required", { status: 400 });
      const scope = parseScope(url.searchParams.get("scope") ?? "offzone");
      if (!scope) return new Response("scope must be onzone or offzone", { status: 400 });
      const ttlSeconds = Number(url.searchParams.get("ttl") ?? "10");
      if (!Number.isFinite(ttlSeconds) || ttlSeconds <= 0) {
        return new Response("ttl must be a positive number", { status: 400 });
      }

      const claimed = await claim(
        caches.default,
        scopedKey(scope, racePath(key), url.origin),
        ttlSeconds,
      );
      return racing({ ...base, claimed, key, scope, seq: url.searchParams.get("seq") });
    }

    if (url.pathname === "/control") {
      if (!run) return new Response("run is required", { status: 400 });
      const scope = parseScope(url.searchParams.get("scope"));
      if (!scope) return new Response("scope must be onzone or offzone", { status: 400 });
      const mode = url.searchParams.get("mode") ?? "read";
      if (mode !== "read" && mode !== "write") {
        return new Response("mode must be read or write", { status: 400 });
      }

      const cache = caches.default;
      const key = scopedKey(scope, controlPath(run), url.origin);

      if (mode === "write") {
        await cache.put(key, record(60, JSON.stringify({ run, writer: base.isolate })));
        const verified = await cache.match(key);
        return racing({ ...base, scope, mode, verified: verified !== undefined });
      }

      const hit = await cache.match(key);
      const stored = hit ? ((await hit.json()) as { writer: string }) : null;
      return racing({ ...base, scope, mode, hit: stored !== null, writer: stored?.writer ?? null });
    }

    return new Response(
      "cache-probe: GET /identity, PUT|GET /entry?run=<id>&ttl=<seconds>,\n" +
        "  POST /race?key=<id>&seq=<n>&scope=<onzone|offzone>&ttl=<seconds>,\n" +
        "  GET /control?run=<id>&scope=<onzone|offzone>&mode=<write|read>&seq=<n>\n",
      { status: 404 },
    );
  },
} satisfies ExportedHandler;
