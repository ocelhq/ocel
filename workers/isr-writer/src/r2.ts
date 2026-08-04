// R2 caps writes to a single key at one per second and answers the loser with
// 429, spelled by the binding as a status, a code, or nothing but a message.
// Both writers in this worker turn on telling that answer apart from a real
// failure and then do opposite things with it — the entry writer reports it, the
// tag clock retries — so the one predicate they share is this.
export function isRateLimited(err: unknown): boolean {
  const e = err as { status?: unknown; code?: unknown; message?: unknown };
  if (e?.status === 429 || e?.code === 429) return true;
  return typeof e?.message === "string" && /429|too many requests/i.test(e.message);
}
