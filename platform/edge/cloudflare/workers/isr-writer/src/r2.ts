export function isRateLimited(err: unknown): boolean {
  const e = err as { status?: unknown; code?: unknown; message?: unknown };
  if (e?.status === 429 || e?.code === 429) return true;
  return typeof e?.message === "string" && /429|too many requests/i.test(e.message);
}
