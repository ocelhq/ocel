import { GetObjectCommand, type S3Client } from "@aws-sdk/client-s3";
import type { ObjectStore } from "@framework/next-image-optimizer/store";
import { readCapped } from "@framework/next-image-optimizer/stream";

export function s3Store(client: S3Client, bucket: string): ObjectStore {
  return {
    async get(key, limit) {
      let output;
      try {
        output = await client.send(new GetObjectCommand({ Bucket: bucket, Key: key }));
      } catch (error) {
        if (isNotFound(error)) return undefined;
        throw error;
      }
      const body = output.Body as AsyncIterable<Uint8Array> | undefined;
      if (!body) return undefined;
      if (output.ContentLength !== undefined && output.ContentLength > limit) {
        (output.Body as { destroy?: () => void })?.destroy?.();
        throw new Error(`object ${key} declares ${output.ContentLength} bytes`);
      }
      return {
        bytes: await readCapped(body, limit),
        cacheControl: output.CacheControl ?? null,
        etag: output.ETag ?? null,
      };
    },
  };
}

function isNotFound(error: unknown): boolean {
  const name = (error as { name?: string })?.name;
  return name === "NoSuchKey" || name === "NotFound";
}
