import { storedCacheTags } from "@framework/next-cache";

export const pathsPerInvalidation = 100;

const tagPathPrefix = "#";

export interface InvalidationPaths {
  batches: string[][];
  dropped: string[];
}

export function invalidationBatches(
  release: string,
  tags: readonly string[],
): InvalidationPaths {
  const { tags: stored, unstorable } = storedCacheTags(release, tags);

  const batches: string[][] = [];
  for (let i = 0; i < stored.length; i += pathsPerInvalidation) {
    batches.push(
      stored.slice(i, i + pathsPerInvalidation).map((tag) => tagPathPrefix + tag),
    );
  }
  return { batches, dropped: unstorable };
}
