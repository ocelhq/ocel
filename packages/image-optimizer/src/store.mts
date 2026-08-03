import { GetObjectCommand, S3Client } from "@aws-sdk/client-s3";
import { readCapped } from "./stream.mjs";

// The account's asset bucket, as this function reads it: one operation, capped.
//
// It is an interface rather than an S3Client so that everything above it is
// testable with no AWS and no network, and so that the byte ceiling is applied
// in one place for both objects this function reads — the config and a local
// image. Next capped its external fetch and left the equivalent local read
// uncapped (CVE-2026-44577); there is one read path here and it is capped.
export interface ObjectStore {
  // Undefined for a key that is not there, which is a 400-shaped outcome (the
  // app does not serve that file) rather than a substrate failure.
  get(key: string, limit: number): Promise<StoredObject | undefined>;
}

export interface StoredObject {
  bytes: Uint8Array;
  cacheControl: string | null;
  etag: string | null;
}

export function s3Store(client: S3Client, bucket: string): ObjectStore {
  return {
    async get(key, limit) {
      let output;
      try {
        output = await client.send(
          new GetObjectCommand({ Bucket: bucket, Key: key }),
        );
      } catch (error) {
        if (isNotFound(error)) return undefined;
        throw error;
      }
      const body = output.Body as AsyncIterable<Uint8Array> | undefined;
      if (!body) return undefined;
      // Checked before the stream is read as well as while reading it: an
      // object whose declared size is already over the ceiling should not cost
      // a single chunk of transfer.
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
