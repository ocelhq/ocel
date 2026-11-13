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

const months = ["jan", "feb", "mar", "apr", "may", "jun", "jul", "aug", "sep", "oct", "nov", "dec"];
const dayNames = "(?:Mon|Tue|Wed|Thu|Fri|Sat|Sun)";
const longDayNames = "(?:Monday|Tuesday|Wednesday|Thursday|Friday|Saturday|Sunday)";
const monthNames = "(Jan|Feb|Mar|Apr|May|Jun|Jul|Aug|Sep|Oct|Nov|Dec)";
const clock = "(\\d{2}):(\\d{2}):(\\d{2})";
const imfFixdate = new RegExp(`^${dayNames}, (\\d{2}) ${monthNames} (\\d{4}) ${clock} GMT$`);
const rfc850Date = new RegExp(`^${longDayNames}, (\\d{2})-${monthNames}-(\\d{2}) ${clock} GMT$`);
const asctimeDate = new RegExp(`^${dayNames} ${monthNames} ([ \\d]\\d) ${clock} (\\d{4})$`);

function buildUtc(
  year: number,
  month: string,
  day: number,
  hour: string,
  minute: string,
  second: string,
): number | undefined {
  const monthIndex = months.indexOf(month.toLowerCase());
  const time = Date.UTC(year, monthIndex, day, Number(hour), Number(minute), Number(second));
  const built = new Date(time);
  const exact =
    built.getUTCFullYear() === year &&
    built.getUTCMonth() === monthIndex &&
    built.getUTCDate() === day &&
    Number(hour) < 24 &&
    Number(minute) < 60 &&
    Number(second) < 60;
  return exact ? time : undefined;
}

function readRfc850Year(twoDigits: number, now: number): number {
  const century = Math.floor(new Date(now).getUTCFullYear() / 100) * 100;
  const year = century + twoDigits;
  const fiftyYears = Date.UTC(new Date(now).getUTCFullYear() + 50, 0, 1);
  return Date.UTC(year, 0, 1) > fiftyYears ? year - 100 : year;
}

type DateParts = [string, string, string, string, string, string];

function readHttpDate(value: string | null, now: number): number | undefined {
  const text = value?.trim();
  if (text === undefined) return undefined;
  const imf = imfFixdate.exec(text)?.slice(1) as DateParts | undefined;
  if (imf) return buildUtc(Number(imf[2]), imf[1], Number(imf[0]), imf[3], imf[4], imf[5]);
  const rfc850 = rfc850Date.exec(text)?.slice(1) as DateParts | undefined;
  if (rfc850) {
    const year = readRfc850Year(Number(rfc850[2]), now);
    return buildUtc(year, rfc850[1], Number(rfc850[0]), rfc850[3], rfc850[4], rfc850[5]);
  }
  const asctime = asctimeDate.exec(text)?.slice(1) as DateParts | undefined;
  if (asctime) {
    return buildUtc(
      Number(asctime[5]),
      asctime[0],
      Number(asctime[1]),
      asctime[2],
      asctime[3],
      asctime[4],
    );
  }
  return undefined;
}

export function readExpiresSeconds(headers: HeaderLookup, now: number): number | undefined {
  const header = headers.get("expires");
  if (header === null) return undefined;
  const expires = readHttpDate(header, now);
  if (expires === undefined) return 0;
  const seconds = Math.floor((expires - (readHttpDate(headers.get("date"), now) ?? now)) / 1000);
  return Math.max(seconds, 0);
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
