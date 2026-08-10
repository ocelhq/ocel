export const MEMO_TTL_MS = 60_000;

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
