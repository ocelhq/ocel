import type { S3Client } from "@aws-sdk/client-s3";
import { readStorageCoordinate, storageCoordinateVar } from "@platform/edge-contract/runtime-env";
import type { StorageCoordinate } from "@platform/edge-contract/storage-key";

export interface ObjectStore {
  client: S3Client;
  bucket: string;
}

export function requireEnv(name: string): string {
  const value = process.env[name];
  if (!value) throw new Error(`ocel cache: ${name} is not set`);
  return value;
}

export function requireStorageCoordinate(): StorageCoordinate {
  const coordinate = readStorageCoordinate(process.env);
  if (coordinate === undefined) throw new Error(`ocel cache: ${storageCoordinateVar} is not set`);
  return coordinate;
}

export function isNotFound(err: any): boolean {
  return err?.name === "NoSuchKey" || err?.$metadata?.httpStatusCode === 404;
}

export async function readBodyText(body: any): Promise<string> {
  if (typeof body?.transformToString === "function") {
    return body.transformToString();
  }
  const chunks: Buffer[] = [];
  for await (const chunk of body) chunks.push(Buffer.from(chunk));
  return Buffer.concat(chunks).toString("utf8");
}
