import type { CacheRule } from "./hosting.mjs";

export const cacheRulesVar = "OCEL_CACHE_RULES";

function matches(rule: CacheRule, pathname: string): boolean {
  if (!rule.path.endsWith("*")) return pathname === rule.path;
  return pathname.startsWith(rule.path.slice(0, -1));
}

export function findCacheRule(
  rules: readonly CacheRule[] | undefined,
  pathname: string,
): CacheRule | undefined {
  return rules?.find((rule) => matches(rule, pathname));
}

export function cacheControlOfRule({ revalidate, expire }: CacheRule): string {
  return expire !== undefined && expire > revalidate
    ? `s-maxage=${revalidate}, stale-while-revalidate=${expire - revalidate}`
    : `s-maxage=${revalidate}`;
}

function refusal(rule: CacheRule): string | undefined {
  const { path, revalidate, expire } = rule;
  if (typeof path !== "string" || !path.startsWith("/") || /[?#]/.test(path)) {
    return "a path starts with a slash and names no query or fragment";
  }
  const stars = path.split("*").length - 1;
  if (stars > 1 || (stars === 1 && !path.endsWith("/*"))) {
    return "a path takes one *, and only as its whole last segment, as in /blog/*";
  }
  if (!Number.isInteger(revalidate) || revalidate < 1) {
    return "revalidate is a whole number of seconds, at least 1";
  }
  if (expire !== undefined && (!Number.isInteger(expire) || expire <= revalidate)) {
    return "expire is a whole number of seconds longer than revalidate";
  }
  return undefined;
}

export function readCacheRules(declared: string | undefined): CacheRule[] {
  if (!declared) return [];
  let parsed: unknown;
  try {
    parsed = JSON.parse(declared);
  } catch {
    throw new Error(`ocel: ${cacheRulesVar} is not JSON`);
  }
  if (!Array.isArray(parsed)) throw new Error(`ocel: ${cacheRulesVar} is not a list of rules`);
  for (const [i, rule] of parsed.entries()) {
    const refused =
      typeof rule === "object" && rule !== null ? refusal(rule) : "a rule is an object";
    if (refused) throw new Error(`ocel: ${cacheRulesVar} rule ${i} is refused: ${refused}`);
  }
  return parsed as CacheRule[];
}
