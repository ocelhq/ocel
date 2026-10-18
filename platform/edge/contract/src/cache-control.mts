export type CacheDirectives = ReadonlyMap<string, string | true>;

export interface HeaderLookup {
  get(name: string): string | null;
}

const standardPolicyHeaders = ["cdn-cache-control", "cache-control"];

export function listSharedPolicyHeaders(edgeHeader: string | undefined): string[] {
  return [
    ...new Set([...(edgeHeader ? [edgeHeader.toLowerCase()] : []), ...standardPolicyHeaders]),
  ];
}

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

const httpDatePattern =
  /^(?:Mon|Tue|Wed|Thu|Fri|Sat|Sun), \d{2} (?:Jan|Feb|Mar|Apr|May|Jun|Jul|Aug|Sep|Oct|Nov|Dec) \d{4} \d{2}:\d{2}:\d{2} GMT$/;

function readHttpDate(value: string | null): number | undefined {
  return value !== null && httpDatePattern.test(value.trim()) ? Date.parse(value) : undefined;
}

export function readExpiresSeconds(headers: HeaderLookup, now: number): number | undefined {
  const expires = readHttpDate(headers.get("expires"));
  if (expires === undefined) return undefined;
  const seconds = Math.floor((expires - (readHttpDate(headers.get("date")) ?? now)) / 1000);
  return seconds > 0 ? seconds : undefined;
}

export function readSharedLifetime(
  directives: CacheDirectives,
  expiresSeconds?: number,
): number | undefined {
  return (
    readDirectiveSeconds(directives, "s-maxage") ??
    readDirectiveSeconds(directives, "max-age") ??
    expiresSeconds
  );
}

function isStoredUnder(directives: CacheDirectives, expiresSeconds?: number): boolean {
  if (refusesSharedStore(directives)) return false;
  return isStoredLifetime(
    readSharedLifetime(directives, expiresSeconds),
    readDirectiveSeconds(directives, "stale-while-revalidate"),
  );
}

export function isStoredBySomeEdge(
  headers: HeaderLookup,
  now: number,
  edgeHeader: string | undefined,
): boolean {
  return listSharedPolicyHeaders(edgeHeader).some((name) => {
    const header = headers.get(name);
    if (name === "cache-control") {
      return isStoredUnder(readCacheDirectives(header), readExpiresSeconds(headers, now));
    }
    return header !== null && isStoredUnder(readCacheDirectives(header));
  });
}
