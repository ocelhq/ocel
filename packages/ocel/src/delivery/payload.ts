export const PAYLOAD_LIMIT_BYTES = 262_144;

export function encodePayload(payload: unknown): Uint8Array {
  const bytes = new TextEncoder().encode(JSON.stringify(payload ?? null));
  if (bytes.byteLength > PAYLOAD_LIMIT_BYTES) {
    throw new Error(
      `a payload is at most ${PAYLOAD_LIMIT_BYTES} bytes (256 KiB) of JSON, and this one is ${bytes.byteLength} bytes`,
    );
  }
  return bytes;
}
