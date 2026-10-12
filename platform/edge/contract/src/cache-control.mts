export type CacheDirectives = ReadonlyMap<string, string | true>;

export const sharedPolicyHeaders = [
  "cloudflare-cdn-cache-control",
  "cdn-cache-control",
  "cache-control",
] as const;

export function readCacheDirectives(header: string | null | undefined): CacheDirectives {
  const directives = new Map<string, string | true>();
  for (const part of header?.split(",") ?? []) {
    const [name, ...value] = part.trim().split("=");
    if (!name) continue;
    directives.set(
      name.trim().toLowerCase(),
      value.length > 0 ? value.join("=").trim().replace(/"/g, "") : true,
    );
  }
  return directives;
}

export function readDirectiveSeconds(
  directives: CacheDirectives,
  name: string,
): number | undefined {
  const value = directives.get(name);
  if (typeof value !== "string" || !/^\d+$/.test(value)) return undefined;
  return Number(value);
}

export function refusesSharedStore(directives: CacheDirectives): boolean {
  return directives.has("private") || directives.has("no-store") || directives.has("no-cache");
}

export function isStoredLifetime(lifetime: number | undefined, staleSeconds?: number): boolean {
  return lifetime !== undefined && (lifetime > 0 || (staleSeconds ?? 0) > 0);
}

export function isStoredBySomeEdge(directives: CacheDirectives): boolean {
  if (refusesSharedStore(directives)) return false;
  const lifetime =
    readDirectiveSeconds(directives, "s-maxage") ?? readDirectiveSeconds(directives, "max-age");
  return isStoredLifetime(lifetime, readDirectiveSeconds(directives, "stale-while-revalidate"));
}
