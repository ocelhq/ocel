import { createHmac, timingSafeEqual } from "node:crypto";
import { z } from "zod";

export const signedFileSchema = z.object({
  key: z.string().min(1),
  name: z.string(),
  size: z.number().int().nonnegative(),
  mimeType: z.string(),
});

export type SignedFile = z.infer<typeof signedFileSchema>;

export function canonicalUploadPayload(sessionId: string, file: SignedFile): string {
  return JSON.stringify({
    sessionId,
    file: {
      key: file.key,
      name: file.name,
      size: file.size,
      mimeType: file.mimeType,
    },
  });
}

export function signUpload(secret: string, sessionId: string, file: SignedFile): string {
  return createHmac("sha256", secret).update(canonicalUploadPayload(sessionId, file)).digest("hex");
}

export function verifyUpload(
  secret: string,
  sessionId: string,
  file: SignedFile,
  signature: string,
): boolean {
  const expected = Buffer.from(signUpload(secret, sessionId, file));
  const presented = Buffer.from(signature);
  return expected.length === presented.length && timingSafeEqual(expected, presented);
}
