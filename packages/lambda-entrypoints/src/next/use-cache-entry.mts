// What the two `use cache` handlers share: the contract they implement, and the
// buffering both are forced into by the fact that Next hands over a one-shot
// stream neither tier can store as-is.

// Next's CacheHandler contract, restated here because the runtime types are not
// a dependency of this package (and the layer must bundle without one).
export interface CacheEntry {
  value: ReadableStream<Uint8Array>;
  tags: string[];
  stale: number;
  timestamp: number;
  expire: number;
  revalidate: number;
}

export const MB = 1024 * 1024;

// One cap for both tiers: what it bounds is the buffer a single set() builds in
// the function's heap, which is the same risk whether those bytes then go to
// memory or to object storage.
function resolveEntryCap(): number {
  const override = Number(process.env.OCEL_USE_CACHE_MAX_ENTRY);
  return override > 0 ? override : 5 * MB;
}

export const maxEntryBytes = resolveEntryCap();

// The clock Next's own handler stamps entry timestamps with. Entry timestamps
// and invalidation watermarks are compared against each other, so they have to
// come from the same clock.
export function now(): number {
  return performance.timeOrigin + performance.now();
}

// Per-key in-flight set tracking, which the CacheHandler contract requires of
// every tier: "If a `get` for the same cache key is called, before the pending
// entry is complete, the cache handler must wait for the `set` operation to
// finish, before returning the entry, instead of returning undefined."
export function pendingSets() {
  const inflight = new Map<string, Promise<void>>();
  return {
    wait(key: string): Promise<void> | undefined {
      return inflight.get(key);
    },
    async run(key: string, fill: () => Promise<void>): Promise<void> {
      let release = (): void => {};
      inflight.set(
        key,
        new Promise<void>((resolve) => {
          release = resolve;
        }),
      );
      try {
        await fill();
      } finally {
        release();
        inflight.delete(key);
      }
    },
  };
}

export function streamOf(bytes: Uint8Array): ReadableStream<Uint8Array> {
  return new ReadableStream({
    start(controller) {
      controller.enqueue(bytes);
      controller.close();
    },
  });
}

// Drains the entry's value into bytes, leaving the caller's copy of the entry
// with an unconsumed stream — the response is still streaming to the user.
//
// Returns null above the per-entry cap: abandoned rather than finished, so one
// oversized entry is never buffered whole just to be rejected. A stream that
// errors part-way throws, because a truncated payload replayed to every later
// reader is worse than a miss.
export async function bufferValue(entry: CacheEntry): Promise<Uint8Array | null> {
  const [value, cloned] = entry.value.tee();
  entry.value = value;

  const reader = cloned.getReader();
  const chunks: Uint8Array[] = [];
  let size = 0;
  for (let chunk; !(chunk = await reader.read()).done; ) {
    size += chunk.value.byteLength;
    if (size > maxEntryBytes) {
      await reader.cancel().catch(() => {});
      return null;
    }
    chunks.push(chunk.value);
  }

  const out = new Uint8Array(size);
  let at = 0;
  for (const chunk of chunks) {
    out.set(chunk, at);
    at += chunk.byteLength;
  }
  return out;
}
