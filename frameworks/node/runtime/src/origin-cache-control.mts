export const uncacheableCacheControl = "private, no-cache, no-store, max-age=0, must-revalidate";

export function isImmutableCacheControl(cacheControl: string | null): boolean {
  return (cacheControl ?? "")
    .split(",")
    .some((directive) => directive.trim().toLowerCase() === "immutable");
}
