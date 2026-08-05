// The claim primitive, and the key shapes it is claimed under.
//
// `claim` is a DELIBERATE MIRROR of claimSentinel in workers/nextjs/src/cache.ts:
// match, and on a miss put — no compare-and-set, and every error admits. The
// window measured through it is a property of that exact shape. If claimSentinel
// ever gains a compare-and-set, or stops failing open, the number this instrument
// produced is void and has to be re-measured against the new shape.

// Production's colo-cache keys all live on synthetic hostnames that belong to no
// zone — refresh.ocel (the L1 sentinel), cache.ocel (entries), isr.ocel (the
// tag-clock front), image.ocel (the optimized-image tier). Whether
// caches.default stores a key whose host is not on the serving zone is
// undocumented and had never been measured, so the scope is a parameter of the
// instrument rather than a constant.
export type KeyScope = "onzone" | "offzone";

// The hostname production's L1 sentinel actually uses. Kept spelled out here
// rather than imported: workers/nextjs is a different package, and a probe that
// silently followed a rename would stop measuring the shape it was pointed at.
const OFF_ZONE_HOST = "https://refresh.ocel";

export const parseScope = (raw: string | null): KeyScope | null =>
  raw === "onzone" || raw === "offzone" ? raw : null;

// The cache key must be a GET Request — Cloudflare throws on any other method in
// cache.put — regardless of the method the racer used to reach the worker.
export function scopedKey(scope: KeyScope, path: string, origin: string): Request {
  const url =
    scope === "offzone" ? `${OFF_ZONE_HOST}${path}` : new URL(path, origin).toString();
  return new Request(url);
}

// An absent jitter parameter is no jitter; anything else has to parse as a
// non-negative number or the request is rejected, since a silently-zero jitter
// would print an un-jittered run under a jittered heading.
export function parseJitterMs(raw: string | null): number | null {
  if (raw === null) return 0;
  const parsed = Number(raw);
  return Number.isFinite(parsed) && parsed >= 0 ? parsed : null;
}

// The jittered admission delay ocelhq-wvag.16 proposes for L1: before claiming,
// wait a uniform draw from [0, J). Production draws it inside refreshOnce, ahead
// of claimSentinel, on a path that has already served a stale response. Here it
// is drawn inside the worker rather than by the driver, so the delay sits on the
// same side of the network as the claim it is meant to spread — a driver-imposed
// delay would also spread the ARRIVALS, and arrival spread suppresses claims by
// itself, which is the effect this instrument has to hold constant.
//
// `random` is injected so the draw's bounds are asserted deterministically. The
// live check that the delay is also SPENT is at the worker's HTTP surface,
// where the draw cannot be pinned.
export const drawDelayMs = (jitterMs: number, random: () => number = Math.random) =>
  jitterMs > 0 ? random() * jitterMs : 0;

// The one spelling in this package, imported by the worker and by both scripts.
export const sleep = (ms: number) =>
  ms > 0 ? new Promise((done) => setTimeout(done, ms)) : Promise.resolve();

export const racePath = (key: string) => `/__race/${encodeURIComponent(key)}`;
export const controlPath = (run: string) => `/__control/${encodeURIComponent(run)}`;

export const record = (ttlSeconds: number, body: BodyInit | null = null) =>
  new Response(body, { headers: { "cache-control": `max-age=${ttlSeconds}` } });

// True when this caller took the claim. Mirrors production line for line.
export async function claim(
  cache: Cache,
  key: Request,
  ttlSeconds: number,
): Promise<boolean> {
  try {
    if (await cache.match(key)) return false;
    await cache.put(key, record(ttlSeconds));
  } catch {
    // Fail open, as production does: admitting twice costs a duplicate render,
    // treating an unreadable cache as another isolate's claim costs the refresh.
  }
  return true;
}
