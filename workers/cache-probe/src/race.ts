export type KeyScope = "onzone" | "offzone";

const OFF_ZONE_HOST = "https://refresh.ocel";

export const parseScope = (raw: string | null): KeyScope | null =>
  raw === "onzone" || raw === "offzone" ? raw : null;

export function scopedKey(scope: KeyScope, path: string, origin: string): Request {
  const url =
    scope === "offzone" ? `${OFF_ZONE_HOST}${path}` : new URL(path, origin).toString();
  return new Request(url);
}

export function parseJitterMs(raw: string | null): number | null {
  if (raw === null) return 0;
  const parsed = Number(raw);
  return Number.isFinite(parsed) && parsed >= 0 ? parsed : null;
}

export const drawDelayMs = (jitterMs: number, random: () => number = Math.random) =>
  jitterMs > 0 ? random() * jitterMs : 0;

export const sleep = (ms: number) =>
  ms > 0 ? new Promise((done) => setTimeout(done, ms)) : Promise.resolve();

export const racePath = (key: string) => `/__race/${encodeURIComponent(key)}`;
export const controlPath = (run: string) => `/__control/${encodeURIComponent(run)}`;

export const record = (ttlSeconds: number, body: BodyInit | null = null) =>
  new Response(body, { headers: { "cache-control": `max-age=${ttlSeconds}` } });

export async function claim(
  cache: Cache,
  key: Request,
  ttlSeconds: number,
): Promise<boolean> {
  try {
    if (await cache.match(key)) return false;
    await cache.put(key, record(ttlSeconds));
  } catch {
  }
  return true;
}
