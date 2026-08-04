// Where a deploy's ISR entries land, and how a rate-limited write is answered.

// Mirrors objectKey in packages/lambda-entrypoints/src/next/cache-store.mts:
// the same objects the edge worker reads back.
const ENTRY_SEGMENT = "cache";
const ENTRY_SUFFIX = ".cache.json";

// The worker derives the whole object key from the deploy's own isrPrefix, so a
// caller's authority is bounded by the prefix it authenticated against and no
// entry key can address another deploy's slice. Anything that could climb out of
// it — an absolute key, a traversal segment, an empty segment — is refused
// rather than normalized.
export function entryObjectKey(isrPrefix: string, key: string): string | null {
  if (key === "" || key.startsWith("/") || key.includes("\\")) return null;
  for (const segment of key.split("/")) {
    if (segment === "" || segment === "." || segment === "..") return null;
  }
  return `${isrPrefix}/${ENTRY_SEGMENT}/${key}${ENTRY_SUFFIX}`;
}

// R2 caps concurrent writes to a single key at one per second and answers the
// loser with 429. Two regenerators racing here each produced a whole fresh
// render, so the loser's write is redundant rather than lost — reporting the
// rate limit and never retrying is what keeps a herd from becoming a retry
// storm.
export type WriteOutcome = "written" | "rate-limited";

export async function writeEntry(
  bucket: R2Bucket,
  objectKey: string,
  body: string,
): Promise<WriteOutcome> {
  try {
    await bucket.put(objectKey, body, {
      httpMetadata: { contentType: "application/json" },
    });
    return "written";
  } catch (err) {
    if (isRateLimited(err)) return "rate-limited";
    throw err;
  }
}

function isRateLimited(err: unknown): boolean {
  const e = err as { status?: unknown; code?: unknown; message?: unknown };
  if (e?.status === 429 || e?.code === 429) return true;
  return typeof e?.message === "string" && /429|too many requests/i.test(e.message);
}
