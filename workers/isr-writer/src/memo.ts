// Each deploy's secret hash, memoized per isolate so a steady stream of entry
// reads and writes costs one DO round trip a minute rather than one apiece. An
// absent hash — never seeded, or retired — is memoized too, so garbage aimed at
// a prefix nobody deployed cannot buy round trips to a single-threaded DO
// either.
//
// Two consequences, both deliberate and both bounded by MEMO_TTL_MS:
//
// - `refreshed` records that a token has already failed against this entry and
//   spent its one registry re-read. That re-read is what lets a redeploy's
//   freshly derived secret in immediately; every further failure is refused off
//   the memo, so a caller holding a bad token cannot drive DO load.
// - a retirement this isolate never handled takes effect here only once the memo
//   lapses, so `destroy` keeps authorizing writes for up to MEMO_TTL_MS in
//   isolates that did not serve it. Closing that window would mean consulting the
//   registry on every write, which is what the memo exists to avoid.
export const MEMO_TTL_MS = 60_000;

// Callers pick the prefixes an isolate is asked about, and an expired memo is
// dropped only when its own key comes back. Oldest-first eviction is what keeps
// a flood of invented prefixes from growing the map without bound; a live
// deploy evicted by one costs a single round trip to come back.
export const CAPACITY = 512;

export interface Memo {
  hash: string | undefined;
  expiresAt: number;
  refreshed: boolean;
}

const memos = new Map<string, Memo>();

export function memoized(isrPrefix: string): Memo | undefined {
  const memo = memos.get(isrPrefix);
  if (memo === undefined) return undefined;
  if (memo.expiresAt <= Date.now()) {
    memos.delete(isrPrefix);
    return undefined;
  }
  return memo;
}

export function memoize(
  isrPrefix: string,
  hash: string | undefined,
  refreshed: boolean,
): Memo {
  if (memos.size >= CAPACITY && !memos.has(isrPrefix)) {
    const oldest = memos.keys().next().value;
    if (oldest !== undefined) memos.delete(oldest);
  }
  const memo = { hash, expiresAt: Date.now() + MEMO_TTL_MS, refreshed };
  memos.set(isrPrefix, memo);
  return memo;
}

export function forget(isrPrefix: string): void {
  memos.delete(isrPrefix);
}
