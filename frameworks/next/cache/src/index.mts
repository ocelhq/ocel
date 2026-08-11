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

const KIND = "isr";
const KEY = "#";
const FIELD = "--";
const SLUG = /^[a-z0-9]([a-z0-9-]*[a-z0-9])?$/;
const RELEASE = /^r[0-9a-f]{8}$/;

function coordinate(
  env: string,
  project: string,
  app: string,
  release: string,
): [string, string, string, string] | null {
  if (![env, project, app].every((f) => SLUG.test(f) && !f.includes(FIELD))) return null;
  return RELEASE.test(release) ? [env, project, app, release] : null;
}

export function tagNamespace(isrPrefix: string): string | null {
  const segments = isrPrefix.split("/");
  if (segments.length !== 5 || segments[4] !== KIND) return null;
  const facts = coordinate(segments[0]!, segments[1]!, segments[2]!, segments[3]!);
  if (facts === null) return null;
  const [env, project, app, release] = facts;
  const stack = [env, app, release].join(FIELD);
  return `PROJECT${KEY}${project}${KEY}STACK${KEY}${stack}${KEY}TAG${KEY}`;
}

export function isrPrefixOf(namespace: string): string | null {
  const tokens = namespace.split(KEY);
  if (tokens.length !== 6) return null;
  if (tokens[0] !== "PROJECT" || tokens[2] !== "STACK" || tokens[4] !== "TAG" || tokens[5] !== "") {
    return null;
  }
  const fields = tokens[3]!.split(FIELD);
  if (fields.length !== 3) return null;
  const facts = coordinate(fields[0]!, tokens[1]!, fields[1]!, fields[2]!);
  if (facts === null) return null;
  const [env, project, app, release] = facts;
  return [env, project, app, release, KIND].join("/");
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
