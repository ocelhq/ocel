// Authenticates the deploy host's writes against the project write-secret
// (bound as secret_text — see cloud/edge/cloudflare/cloudflare.go
// scriptBindings). Digesting both sides before comparing equalizes their
// length and avoids leaking any information through an early-exit compare,
// so a wrong-length or wrong-content secret cost the same constant time.
export async function authorized(
  request: Request,
  secret: string,
): Promise<boolean> {
  const header = request.headers.get("authorization") ?? "";
  const match = /^Bearer (.+)$/.exec(header);
  if (!match) return false;
  return constantTimeEqual(match[1], secret);
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
