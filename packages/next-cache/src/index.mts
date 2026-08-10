export {
  isGuardRejection,
  tagRecordUpdate,
  tagSortKey,
  type TagAttribute,
  type TagRecordUpdate,
  type TagUpdateItem,
} from "./tag-index.mjs";
export {
  latest,
  mergeRecord,
  mergeSnapshot,
  publishTagSnapshot,
  readableSnapshot,
  type StoredTagSnapshot,
  type TagSnapshotStore,
} from "./tag-snapshot.mjs";
export type { EdgeCacheRpc, FetchCacheEntry } from "./edge-cache-rpc.mjs";
export { cacheKey, variantHeadersFile } from "./naming.mjs";

export interface CacheEntryFile {
  lastModified: number;
  value: Record<string, any>;
  cacheControl?: { revalidate?: number | false; expire?: number };
}

export interface TagRecord {
  stale?: number;
  expired?: number;
}

export interface TagSnapshot {
  version: 1;
  deployedAt: number;
  generatedAt: number;
  records: Record<string, TagRecord>;
}

export function tagSnapshotKey(prefix: string): string {
  return `${prefix}/tag-clock.json`;
}

export function tagNamespace(prefix: string): string {
  return `TAG#${prefix.replaceAll("/", "#")}#`;
}

const PREFIX_SEGMENTS = 4;

export function isrPrefixOf(namespace: string): string | null {
  if (!namespace.startsWith("TAG#") || !namespace.endsWith("#")) return null;
  const segments = namespace.slice("TAG#".length, -1).split("#");
  if (segments.length !== PREFIX_SEGMENTS || segments.some((s) => s === "")) return null;
  return segments.join("/");
}

const TAGS_HEADER = "x-next-cache-tags";

export function base64ToBytes(b64: string): Uint8Array {
  const binary = atob(b64);
  const bytes = new Uint8Array(binary.length);
  for (let i = 0; i < binary.length; i++) bytes[i] = binary.charCodeAt(i);
  return bytes;
}

export function bytesToBase64(bytes: Uint8Array): string {
  let binary = "";
  for (let i = 0; i < bytes.length; i++) binary += String.fromCharCode(bytes[i]!);
  return btoa(binary);
}

export function entryObjectKey(isrPrefix: string, key: string): string | null {
  if (key === "" || key.startsWith("/") || key.includes("\\")) return null;
  for (const segment of key.split("/")) {
    if (segment === "." || segment === "..") return null;
  }
  return `${isrPrefix}/cache/${key}.cache.json`;
}

export const entryMissHeader = "ocel-isr-entry-miss";

export function tagsOf(value: Record<string, any>, ctx: any): string[] {
  if (value?.kind === "FETCH") {
    return [
      ...new Set([...(ctx?.tags ?? []), ...(ctx?.softTags ?? []), ...(value.tags ?? [])]),
    ];
  }
  const header = value?.headers?.[TAGS_HEADER];
  return typeof header === "string" && header.length > 0 ? header.split(",") : [];
}

export function areTagsExpired(
  tags: string[],
  records: Map<string, TagRecord>,
  timestamp: number,
  now: number,
): boolean {
  for (const tag of tags) {
    const expiredAt = records.get(tag)?.expired;
    if (typeof expiredAt !== "number") continue;
    if (expiredAt <= now && expiredAt > timestamp) return true;
  }
  return false;
}

export function deserialize(value: Record<string, any>): Record<string, any> {
  const out: Record<string, any> = { ...value };
  if (value.kind === "APP_ROUTE" && typeof value.body === "string") {
    out.body = base64ToBytes(value.body);
  }
  if (value.kind === "APP_PAGE") {
    out.rscData = value.rscData ? base64ToBytes(value.rscData) : undefined;
    if (value.segmentData) {
      out.segmentData = new Map(
        Object.entries(value.segmentData as Record<string, string>).map(
          ([path, b64]) => [path, base64ToBytes(b64)],
        ),
      );
    }
  }
  return out;
}
