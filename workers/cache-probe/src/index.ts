import { isolateId } from "./isolate";

// A deployed instrument, not production machinery. It exposes the smallest
// surface that lets a runner outside Cloudflare decide three things it cannot
// decide from inside one request: whether a cache.put made by one isolate is
// readable by another isolate in the same colo, how long an entry written with a
// short max-age actually survives, and how many isolates a colo spreads a burst
// across.
//
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
        const started = Date.now();
        await cache.put(
          key,
          Response.json(sentinel, {
            headers: { "cache-control": `max-age=${ttlSeconds}` },
          }),
        );
        return Response.json({ ...base, sentinel, putMs: Date.now() - started });
      }

      if (request.method === "GET") {
        const started = Date.now();
        const hit = await cache.match(key);
        const lookupMs = Date.now() - started;
        const sentinel = hit ? ((await hit.json()) as Sentinel) : null;
        return Response.json({
          ...base,
          hit: sentinel !== null,
          writer: sentinel?.writer ?? null,
          requestedTtlSeconds: sentinel?.ttlSeconds ?? null,
          // Cloudflare's own view of how long it has held the entry, when it
          // reports one. Independent of any clock this probe reads.
          age: hit?.headers.get("age"),
          cacheControl: hit?.headers.get("cache-control"),
          lookupMs,
        });
      }

      if (request.method === "DELETE") {
        return Response.json({ ...base, deleted: await cache.delete(key) });
      }

      return new Response("Method Not Allowed", { status: 405 });
    }

    return new Response(
      "cache-probe: GET /identity, PUT|GET|DELETE /entry?run=<id>&ttl=<seconds>\n",
      { status: 404 },
    );
  },
} satisfies ExportedHandler;
