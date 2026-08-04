import { bearer, matchesSecret } from "@ocel/worker-auth";

// Authenticates the deploy host's writes against the project write-secret
// (bound as secret_text — see cloud/edge/cloudflare/cloudflare.go
// scriptBindings).
export async function authorized(request: Request, secret: string): Promise<boolean> {
  const token = bearer(request);
  return token !== null && (await matchesSecret(token, secret));
}
