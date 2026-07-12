/**
 * Wraps the uploader name alongside the middleware's return in the session's
 * opaque metadata bytes, so the callback (which carries no metadata on the
 * wire) can resolve which uploader's onUploadComplete to run.
 */
export interface MetadataEnvelope {
  uploader: string;
  metadata: unknown;
}

export function encodeMetadata(envelope: MetadataEnvelope): Uint8Array {
  return new TextEncoder().encode(JSON.stringify(envelope));
}

export function decodeMetadata(bytes: Uint8Array): MetadataEnvelope {
  return JSON.parse(new TextDecoder().decode(bytes)) as MetadataEnvelope;
}
