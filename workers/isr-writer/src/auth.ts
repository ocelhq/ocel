// The writer's credential primitives. Two kinds of caller reach the worker: the
// deploy host, holding the account-level bootstrap secret in plaintext on both
// sides, and a deployed Lambda, holding a per-deploy secret whose SHA-256 hash
// is the only copy Cloudflare stores. Both compare digests, so neither compare
// leaks anything through an early exit.

export function bearer(request: Request): string | null {
  const header = request.headers.get("authorization") ?? "";
  const match = /^Bearer (.+)$/.exec(header);
  return match ? match[1] : null;
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

// Compares a presented token against a secret held in plaintext (the bootstrap
// credential): digesting both sides equalizes their length before the compare.
export async function matchesSecret(token: string, secret: string): Promise<boolean> {
  const [presented, expected] = await Promise.all([sha256Hex(token), sha256Hex(secret)]);
  return timingSafeEqual(presented, expected);
}

// Compares a presented token against a stored SHA-256 hash (a deploy's write
// secret), which is all the DO ever holds.
export async function matchesHash(token: string, hash: string): Promise<boolean> {
  return timingSafeEqual(await sha256Hex(token), hash);
}

// A hash is only ever accepted from the deploy host in this shape, so a
// malformed one is rejected at the door rather than stored and then never
// matched by anything.
export function isSecretHash(value: unknown): value is string {
  return typeof value === "string" && /^[0-9a-f]{64}$/.test(value);
}
