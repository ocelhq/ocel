export interface CacheEntry {
  value: ReadableStream<Uint8Array>;
  tags: string[];
  stale: number;
  timestamp: number;
  expire: number;
  revalidate: number;
}

export const MB = 1024 * 1024;

function resolveEntryCap(): number {
  const override = Number(process.env.OCEL_USE_CACHE_MAX_ENTRY);
  return override > 0 ? override : 5 * MB;
}

export const maxEntryBytes = resolveEntryCap();

export function now(): number {
  return performance.timeOrigin + performance.now();
}

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
