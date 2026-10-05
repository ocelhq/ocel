import { createHash } from "node:crypto";
import { createReadStream } from "node:fs";
import { readFile, stat } from "node:fs/promises";
import { join, normalize, sep } from "node:path";
import { Readable } from "node:stream";
import type { ObjectStore } from "@framework/next-image-optimizer/store";
import type { AssetBucket, AssetObject } from "@framework/next-router/assets";

const assetsSegment = "assets";

function releaseRoot(assetPrefix: string): string {
  return assetPrefix.endsWith(assetsSegment)
    ? assetPrefix.slice(0, -assetsSegment.length)
    : `${assetPrefix}/`;
}

function diskFiles(dir: string, assetPrefix: string): (key: string) => Promise<string | null> {
  const root = releaseRoot(assetPrefix);
  const within = dir.endsWith(sep) ? dir : dir + sep;
  return async (key) => {
    if (!key.startsWith(root) || key.includes("\0")) return null;
    const file = normalize(join(dir, key.slice(root.length)));
    if (!file.startsWith(within)) return null;
    try {
      return (await stat(file)).isFile() ? file : null;
    } catch {
      return null;
    }
  };
}

export function diskAssetBucket(dir: string, assetPrefix: string): AssetBucket {
  const fileOf = diskFiles(dir, assetPrefix);
  const etags = new Map<string, Promise<string>>();
  const etagOf = (file: string): Promise<string> => {
    let etag = etags.get(file);
    if (!etag) {
      etag = readFile(file).then(
        (body) => `"${createHash("sha256").update(body).digest("base64url")}"`,
      );
      etags.set(file, etag);
    }
    return etag;
  };
  return {
    async get(key): Promise<AssetObject | null> {
      const file = await fileOf(key);
      if (!file) return null;
      return {
        body: Readable.toWeb(createReadStream(file)) as ReadableStream,
        httpEtag: await etagOf(file),
      };
    },
  };
}

export function diskObjectStore(dir: string, assetPrefix: string): ObjectStore {
  const fileOf = diskFiles(dir, assetPrefix);
  return {
    async get(key, limit) {
      const file = await fileOf(key);
      if (!file) return undefined;
      const { size } = await stat(file);
      if (size > limit) throw new Error(`object ${key} holds ${size} bytes`);
      return { bytes: new Uint8Array(await readFile(file)), cacheControl: null, etag: null };
    },
  };
}
