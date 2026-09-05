export const CACHE_HEADER = "x-ocel-cache";

const ONE_YEAR_SECONDS = 31536000;

export const DYNAMIC_CACHE_CONTROL = "private, no-cache, no-store, max-age=0, must-revalidate";

export const IMMUTABLE_CACHE_CONTROL = `public, max-age=${ONE_YEAR_SECONDS}, immutable`;

export const SERVED_CACHE_CONTROL = "public, max-age=0, must-revalidate";

export const ROUTER_VARY = [
  "RSC",
  "Next-Router-State-Tree",
  "Next-Router-Prefetch",
  "Next-Router-Segment-Prefetch",
];

export type Tier = "HIT" | "MISS" | "STALE" | "PRERENDER" | "BYPASS";

export const TIERS: Tier[] = ["HIT", "MISS", "STALE", "PRERENDER", "BYPASS"];

export const CACHED: Tier[] = ["HIT", "STALE", "PRERENDER"];

export const UNCACHED: Tier[] = ["MISS", "BYPASS"];

export function cacheControlFor(revalidate: number | false): string {
  return revalidate === 0 ? DYNAMIC_CACHE_CONTROL : SERVED_CACHE_CONTROL;
}

export function imageCacheControl(seconds: number): string {
  return `public, max-age=${seconds}, must-revalidate`;
}

export function directivesOf(value: string | null): Set<string> {
  return new Set(
    (value ?? "")
      .split(",")
      .map((directive) => directive.trim().toLowerCase())
      .filter((directive) => directive !== ""),
  );
}

export function sameDirectives(value: string | null, expected: string): boolean {
  const listed = directivesOf(value);
  const wanted = directivesOf(expected);
  return listed.size === wanted.size && [...wanted].every((directive) => listed.has(directive));
}

export function variesOn(vary: string | null, names: string[]): boolean {
  const listed = new Set(
    (vary ?? "")
      .split(",")
      .map((name) => name.trim().toLowerCase())
      .filter((name) => name !== ""),
  );
  return names.every((name) => listed.has(name.toLowerCase()));
}

export function tierOf(res: Response): Tier {
  const stamped = res.headers.get(CACHE_HEADER);
  if (!stamped) {
    throw new Error(`the response carried no ${CACHE_HEADER}`);
  }
  const tier = TIERS.find((known) => known === stamped);
  if (!tier) {
    throw new Error(`${CACHE_HEADER} was ${stamped}, which names no tier this contract knows`);
  }
  return tier;
}
