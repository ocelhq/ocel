export interface InstanceCache {
  read<T>(key: string): T | undefined;
  write(key: string, value: unknown, bytes: number): void;
}

const MB = 1024 * 1024;

const defaultBytes = 50 * MB;

const memoryShare = 0.1;

export function instanceCacheBytes(memoryBytes?: number): number {
  if (memoryBytes !== undefined && memoryBytes > 0) return Math.floor(memoryBytes * memoryShare);
  return defaultBytes;
}

interface Slot {
  value: unknown;
  bytes: number;
}

export function newInstanceCache(maxBytes: number): InstanceCache {
  const slots = new Map<string, Slot>();
  let usedBytes = 0;

  return {
    read<T>(key: string): T | undefined {
      const slot = slots.get(key);
      if (!slot) return undefined;
      slots.delete(key);
      slots.set(key, slot);
      return slot.value as T;
    },

    write(key: string, value: unknown, bytes: number): void {
      const existing = slots.get(key);
      if (existing) {
        usedBytes -= existing.bytes;
        slots.delete(key);
      }
      if (bytes > maxBytes) return;

      slots.set(key, { value, bytes });
      usedBytes += bytes;

      while (usedBytes > maxBytes) {
        const oldest = slots.keys().next().value;
        if (oldest === undefined) break;
        usedBytes -= slots.get(oldest)!.bytes;
        slots.delete(oldest);
      }
    },
  };
}
