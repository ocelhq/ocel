import { createHash } from "node:crypto";
import { createReadStream } from "node:fs";
import { readFile, stat } from "node:fs/promises";
import { join, normalize, sep } from "node:path";
import { Readable } from "node:stream";
import type { AssetBucket, AssetObject } from "@framework/next-router/assets";

const assetsSegment = "assets";

function releaseRoot(assetPrefix: string): string {
  return assetPrefix.endsWith(assetsSegment)
    ? assetPrefix.slice(0, -assetsSegment.length)
    : `${assetPrefix}/`;
}

function pathWithin(dir: string, rel: string): string | null {
  if (rel.includes("\0")) return null;
  const full = normalize(join(dir, rel));
  return full.startsWith(dir.endsWith(sep) ? dir : dir + sep) ? full : null;
}

export function diskAssetBucket(dir: string, assetPrefix: string): AssetBucket {
  const root = releaseRoot(assetPrefix);
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
      if (!key.startsWith(root)) return null;
      const file = pathWithin(dir, key.slice(root.length));
      if (!file) return null;
      try {
        if (!(await stat(file)).isFile()) return null;
        return {
          body: Readable.toWeb(createReadStream(file)) as ReadableStream,
          httpEtag: await etagOf(file),
        };
      } catch {
        return null;
      }
    },
  };
}
