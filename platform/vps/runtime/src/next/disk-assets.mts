import { createReadStream } from "node:fs";
import { readFile, stat } from "node:fs/promises";
import { join, resolve, sep } from "node:path";
import { Readable } from "node:stream";
import type { ObjectStore } from "@framework/next-image-optimizer/store";
import type { AssetBucket, AssetObject } from "@framework/next-router/assets";
import {
  objectKey,
  type StorageCoordinate,
  storagePrefix,
} from "@platform/edge-contract/storage-key";

const staticDir = "static";

const imageConfigFile = "image-config.json";

function isMissing(error: unknown): boolean {
  const code = (error as NodeJS.ErrnoException).code;
  return code === "ENOENT" || code === "ENOTDIR";
}

function resolveAsset(dir: string, storage: StorageCoordinate, key: string): string | undefined {
  const root = resolve(dir);
  const prefix = storagePrefix("assets", storage);
  if (!key.startsWith(prefix)) return undefined;
  const file = resolve(join(root, key.slice(prefix.length)));
  return file.startsWith(root + sep) ? file : undefined;
}

async function statFile(file: string) {
  try {
    const info = await stat(file);
    return info.isFile() ? info : undefined;
  } catch (error) {
    if (isMissing(error)) return undefined;
    throw error;
  }
}

export function newDiskAssetBucket(buildDir: string, storage: StorageCoordinate): AssetBucket {
  const dir = join(buildDir, staticDir);
  return {
    async get(key: string): Promise<AssetObject | null> {
      const file = resolveAsset(dir, storage, key);
      const info = file && (await statFile(file));
      if (!file || !info) return null;
      return {
        body: Readable.toWeb(createReadStream(file)) as ReadableStream,
        httpEtag: `"${info.size.toString(16)}-${Math.trunc(info.mtimeMs).toString(16)}"`,
      };
    },
  };
}

export function newDiskObjectStore(buildDir: string, storage: StorageCoordinate): ObjectStore {
  const imageConfigKey = objectKey("image-config", storage, imageConfigFile);
  return {
    async get(key, limit) {
      const file =
        key === imageConfigKey
          ? join(buildDir, imageConfigFile)
          : resolveAsset(join(buildDir, staticDir), storage, key);
      const info = file && (await statFile(file));
      if (!file || !info) return undefined;
      if (info.size > limit) throw new Error(`object ${key} holds ${info.size} bytes`);
      return { bytes: new Uint8Array(await readFile(file)), cacheControl: null, etag: null };
    },
  };
}
