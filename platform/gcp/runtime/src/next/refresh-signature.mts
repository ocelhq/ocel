import { createHmac, timingSafeEqual } from "node:crypto";

export const refreshSignatureHeader = "x-ocel-refresh-signature";

export function signRefreshTask(secret: string, body: Buffer): string {
  return createHmac("sha256", secret).update(body).digest("hex");
}

export function isRefreshTaskSignedBy(
  secret: string,
  body: Buffer,
  signature: string | string[] | undefined,
): boolean {
  if (typeof signature !== "string" || !/^[0-9a-f]{64}$/.test(signature)) return false;
  const expected = createHmac("sha256", secret).update(body).digest();
  return timingSafeEqual(expected, Buffer.from(signature, "hex"));
}
