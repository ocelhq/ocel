import { bearer, matchesSecret } from "@platform/cf-auth";

export async function authorized(request: Request, secret: string): Promise<boolean> {
  const token = bearer(request);
  return token !== null && (await matchesSecret(token, secret));
}
