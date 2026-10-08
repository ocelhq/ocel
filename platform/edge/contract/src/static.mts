import type { Static } from "./hosting.mjs";

export const IMMUTABLE_CACHE_CONTROL = "public, max-age=31536000, immutable";
export const REVALIDATE_CACHE_CONTROL = "public, max-age=0, must-revalidate";

export function isImmutable(rules: Static | undefined, pathname: string): boolean {
  if (!rules) return false;
  const under = (prefix: string) => pathname.startsWith(prefix);
  return rules.immutablePrefixes.some(under) && !(rules.mustRevalidatePrefixes ?? []).some(under);
}

export function cacheControlFor(rules: Static | undefined, pathname: string): string {
  return isImmutable(rules, pathname) ? IMMUTABLE_CACHE_CONTROL : REVALIDATE_CACHE_CONTROL;
}

export function isUnderImmutablePrefix(rules: Static | undefined, pathname: string): boolean {
  return (rules?.immutablePrefixes ?? []).some((prefix) => pathname.startsWith(prefix));
}
