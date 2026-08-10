export class TooLargeError extends Error {
  constructor(readonly limit: number) {
    super(`response exceeded ${limit} bytes`);
    this.name = "TooLargeError";
  }
}

export async function readCapped(
  source: AsyncIterable<Uint8Array>,
  limit: number,
): Promise<Uint8Array> {
  const chunks: Uint8Array[] = [];
  let total = 0;
  for await (const chunk of source) {
    total += chunk.byteLength;
    if (total > limit) throw new TooLargeError(limit);
    chunks.push(chunk);
  }
  const out = new Uint8Array(total);
  let offset = 0;
  for (const chunk of chunks) {
    out.set(chunk, offset);
    offset += chunk.byteLength;
  }
  return out;
}
