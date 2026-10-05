export interface ObjectStore {
  get(key: string, limit: number): Promise<StoredObject | undefined>;
}

export interface StoredObject {
  bytes: Uint8Array;
  cacheControl: string | null;
  etag: string | null;
}
