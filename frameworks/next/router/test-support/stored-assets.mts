import { type StorageCoordinate, storagePrefix } from "@platform/edge-contract/storage-key";

export const TEST_STORAGE: StorageCoordinate = {
  project: "p1",
  env: "prod",
  app: "web",
  release: "r0a1b2c3d",
};

export function storedPathname(key: string): string | undefined {
  const prefix = storagePrefix("assets", TEST_STORAGE);
  return key.startsWith(prefix) ? `/${key.slice(prefix.length)}` : undefined;
}
