// Authenticates the deploy host's writes against the project write-secret
// (bound as secret_text — see cloud/edge/cloudflare/cloudflare.go
// scriptBindings). Digesting both sides before comparing equalizes their
// length and avoids leaking any information through an early-exit compare,
// so a wrong-length or wrong-content secret cost the same constant time.
export async function authorized(
  request: Request,
  secret: string,
): Promise<boolean> {
  const token = bearer(request);
  if (token === null) return false;
  return constantTimeEqual(token, secret);
}

// Extracts the bearer token from a request's Authorization header, or null when
// it is missing or not a Bearer credential. The per-project auth path
// (index.ts) pulls the token out here and hands it to the DO instance, which
// compares it against its own stored secret.
export function bearer(request: Request): string | null {
  const header = request.headers.get("authorization") ?? "";
  const match = /^Bearer (.+)$/.exec(header);
  return match ? match[1] : null;
}

export async function constantTimeEqual(a: string, b: string): Promise<boolean> {
  const enc = new TextEncoder();
  const [digestA, digestB] = await Promise.all([
    crypto.subtle.digest("SHA-256", enc.encode(a)),
    crypto.subtle.digest("SHA-256", enc.encode(b)),
  ]);
  const bytesA = new Uint8Array(digestA);
  const bytesB = new Uint8Array(digestB);
  let diff = 0;
  for (let i = 0; i < bytesA.length; i++) diff |= bytesA[i] ^ bytesB[i];
  return diff === 0;
}
