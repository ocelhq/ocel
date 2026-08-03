// Reading a body under a hard byte ceiling.
//
// The ceiling is enforced as the bytes arrive, never after. `arrayBuffer()` on
// an untrusted response allocates whatever the sender decides to send and only
// then lets us measure it, which on a function with a fixed memory limit is an
// OOM the sender controls the timing of. Counting incrementally turns the same
// input into a cheap abort.
//
// Everything that reads bytes here goes through this — the remote fetch, the
// S3 read of a local image, and the S3 read of the config. Next capped the
// external path first and left the local one uncapped, and earned
// CVE-2026-44577 for the gap.

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
    // Thrown before the chunk is retained, so the peak is one chunk over the
    // limit and not one body over it.
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
