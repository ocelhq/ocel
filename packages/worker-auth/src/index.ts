// The credential primitives every account-level worker authenticates with.
// Callers hold a bearer token and the worker holds either the same secret in
// plaintext or its SHA-256 hash; both compares run over digests, so neither
// leaks anything through an early exit and neither leaks a length.

export function bearer(request: Request): string | null {
  const header = request.headers.get("authorization") ?? "";
  const match = /^Bearer (.+)$/.exec(header);
  return match?.[1] ?? null;
}

export async function sha256Hex(value: string): Promise<string> {
  const digest = await crypto.subtle.digest("SHA-256", new TextEncoder().encode(value));
  return [...new Uint8Array(digest)]
    .map((byte) => byte.toString(16).padStart(2, "0"))
    .join("");
}

// Both operands are hex digests of fixed width, so the length guard reveals
// nothing a caller does not already know.
export function timingSafeEqual(a: string, b: string): boolean {
  if (a.length !== b.length) return false;
  let diff = 0;
  for (let i = 0; i < a.length; i++) diff |= a.charCodeAt(i) ^ b.charCodeAt(i);
  return diff === 0;
}

// Compares a presented token against a secret held in plaintext: digesting both
// sides equalizes their length before the compare.
export async function matchesSecret(token: string, secret: string): Promise<boolean> {
  const [presented, expected] = await Promise.all([sha256Hex(token), sha256Hex(secret)]);
  return timingSafeEqual(presented, expected);
}

// Compares a presented token against a stored SHA-256 hash, which is all a
// worker that never sees the plaintext holds.
export async function matchesHash(token: string, hash: string): Promise<boolean> {
  return timingSafeEqual(await sha256Hex(token), hash);
}
