import type { CacheTagLimits } from "@platform/edge-contract/cache-tags";

export function readEdgeRunsCode(): boolean {
  return process.env.OCEL_EDGE_RUNS_CODE === "1";
}

function isPositiveWholeNumber(value: unknown): value is number {
  return typeof value === "number" && Number.isSafeInteger(value) && value > 0;
}

export function readEdgeCacheTagLimits(): CacheTagLimits | undefined {
  const declared = process.env.OCEL_EDGE_TAG_LIMITS;
  if (!declared) return undefined;
  let limits: Partial<CacheTagLimits> | null = null;
  try {
    limits = JSON.parse(declared);
  } catch {}
  if (
    !isPositiveWholeNumber(limits?.tagBytes) ||
    !isPositiveWholeNumber(limits.tags) ||
    !isPositiveWholeNumber(limits.headerBytes)
  ) {
    throw new Error(
      `ocel: OCEL_EDGE_TAG_LIMITS is "${declared}", and an edge's cache tag limits are a JSON object of positive whole tagBytes, tags and headerBytes`,
    );
  }
  return { tagBytes: limits.tagBytes, tags: limits.tags, headerBytes: limits.headerBytes };
}

function splitCommaSeparated(declared: string | undefined): Set<string> {
  return new Set(
    (declared ?? "")
      .split(",")
      .map((name) => name.trim())
      .filter(Boolean),
  );
}

export function readWaivedNeeds(): Set<string> {
  return splitCommaSeparated(process.env.OCEL_ALLOW_DEGRADED);
}

export function readPublicKeys(): Set<string> {
  return splitCommaSeparated(process.env.OCEL_PUBLIC_KEYS);
}

export function readMaxFunctionBytes(): number | undefined {
  const declared = process.env.OCEL_MAX_FUNCTION_BYTES;
  if (!declared) return undefined;
  const bytes = Number(declared);
  if (!Number.isSafeInteger(bytes) || bytes <= 0) {
    throw new Error(
      `ocel: OCEL_MAX_FUNCTION_BYTES is "${declared}", and a function's size budget is a positive whole number of bytes`,
    );
  }
  return bytes;
}
