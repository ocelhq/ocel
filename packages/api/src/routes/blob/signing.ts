import { createHmac, timingSafeEqual } from "node:crypto";

// The file identity a completion callback signs over. Mirrors the proto
// CompletedFile; size is a plain JSON number here (files are single-PUT with a
// documented ceiling, well within a safe integer).
export interface SignedFile {
  key: string;
  name: string;
  size: number;
  mimeType: string;
}

// The canonical payload the per-session HMAC covers. A fixed field order is
// the whole contract: the detector (which signs) and VerifyUploadSignature
// (which re-derives and compares) must serialize identically, so both go
// through this one function. Never reorder fields.
export function canonicalUploadPayload(
  sessionId: string,
  file: SignedFile,
): string {
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

// HMAC-SHA256 over the canonical payload, hex-encoded. This is the signature
// carried on op=callback; the secret stays in the store.
export function signUpload(
  secret: string,
  sessionId: string,
  file: SignedFile,
): string {
  return createHmac("sha256", secret)
    .update(canonicalUploadPayload(sessionId, file))
    .digest("hex");
}

// Constant-time comparison of a presented signature against the re-derived
// one. Returns false on any length mismatch (timingSafeEqual throws otherwise).
export function verifyUpload(
  secret: string,
  sessionId: string,
  file: SignedFile,
  signature: string,
): boolean {
  const expected = Buffer.from(signUpload(secret, sessionId, file));
  const presented = Buffer.from(signature);
  return (
    expected.length === presented.length &&
    timingSafeEqual(expected, presented)
  );
}
