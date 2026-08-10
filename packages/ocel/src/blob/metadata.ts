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
