import { createRequire } from "node:module";
import { join } from "node:path";
import { nextFileSystemCacheModule } from "@framework/next-protocol/launcher";
import type { PurgeTags } from "@framework/node-runtime/tag-records";
import type { TagRecord } from "@platform/edge-contract/purge";

const nextCacheHandlersKey = Symbol.for("@next/cache-handlers");

interface RevalidateDurations {
  expire?: number;
}

interface FileSystemCache {
  revalidateTag(tags: string | string[], durations?: RevalidateDurations): Promise<void>;
}

type FileSystemCacheClass = new (ctx: { flushToDisk?: boolean }) => FileSystemCache;

function newRevalidatedRecord(durations: RevalidateDurations | undefined, at: number): TagRecord {
  if (!durations) return { expired: at };
  return {
    stale: at,
    ...(durations.expire !== undefined && { expired: at + durations.expire * 1000 }),
  };
}

export function installPurgingFileSystemCache(projectDir: string, purge: PurgeTags): void {
  const { default: NextFileSystemCache } = createRequire(join(projectDir, "package.json"))(
    nextFileSystemCacheModule,
  ) as { default: FileSystemCacheClass };
  (globalThis as Record<symbol, unknown>)[nextCacheHandlersKey] = {
    FetchCache: class extends NextFileSystemCache {
      constructor(ctx: { flushToDisk?: boolean }) {
        super({ ...ctx, flushToDisk: false });
      }

      async revalidateTag(tags: string | string[], durations?: RevalidateDurations): Promise<void> {
        await super.revalidateTag(tags, durations);
        const record = newRevalidatedRecord(durations, Date.now());
        await purge(new Map([tags].flat().map((tag) => [tag, record])));
      }
    },
  };
}
