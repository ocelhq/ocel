export type WriteOutcome = "written" | "rate-limited";

// R2 caps concurrent writes to a single key at one per second and answers the
// loser with 429. Two regenerators racing here each produced a whole fresh
// render, so the loser's write is redundant rather than lost — reporting the
// rate limit and never retrying is what keeps a herd from becoming a retry
// storm.
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

// An absent object is a miss, not a failure: the caller renders. Nothing else is
// swallowed here — the caller's own fail-open decides what a real R2 failure
// costs, and it can only decide that if it is told.
export async function readEntry(
  bucket: R2Bucket,
  objectKey: string,
): Promise<string | null> {
  const object = await bucket.get(objectKey);
  return object === null ? null : object.text();
}
