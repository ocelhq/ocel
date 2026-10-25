import type { PurgeEntry, TagRecord } from "@platform/edge-contract/purge";

export type PurgeTags = (tags: ReadonlyMap<string, TagRecord>) => Promise<void>;

export type TagRecordWriter = (entries: readonly PurgeEntry[]) => Promise<void>;

let writer: TagRecordWriter | undefined;

export function setTagRecordWriter(write: TagRecordWriter | undefined): void {
  writer = write;
}

export function findTagRecordWriter(): TagRecordWriter | undefined {
  return writer;
}

export const tagWritesInFlight = 8;

export async function runTagWrites<T>(
  items: readonly T[],
  write: (item: T) => Promise<unknown>,
): Promise<unknown[]> {
  const failures: unknown[] = [];
  let next = 0;
  const writeEach = async () => {
    while (next < items.length) {
      try {
        await write(items[next++]!);
      } catch (error) {
        failures.push(error);
      }
    }
  };
  await Promise.all(Array.from({ length: Math.min(tagWritesInFlight, items.length) }, writeEach));
  return failures;
}
