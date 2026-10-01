/** The most bytes of JSON a payload may take. */
export const PAYLOAD_LIMIT_BYTES = 262_144;

/** A payload's JSON text as bytes, with `undefined` sent as `null`. Throws over {@link PAYLOAD_LIMIT_BYTES}. */
export function encodePayload(payload: unknown): Uint8Array {
  const bytes = new TextEncoder().encode(JSON.stringify(payload ?? null));
  if (bytes.byteLength > PAYLOAD_LIMIT_BYTES) {
    throw new Error(
      `a payload is at most ${PAYLOAD_LIMIT_BYTES} bytes (256 KiB) of JSON, and this one is ${bytes.byteLength} bytes`,
    );
  }
  return bytes;
}

/** The value JSON text holds; `null` when there is no text. */
export function decodeJson(bytes: Uint8Array): unknown {
  return bytes.byteLength > 0 ? JSON.parse(new TextDecoder().decode(bytes)) : null;
}
