import type { S3Client } from "@aws-sdk/client-s3";
import {
  parseStorageCoordinate,
  type StorageCoordinate,
} from "@platform/edge-contract/storage-key";
import { newS3Client } from "../cache/sdk-clients.mjs";

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
  const value = requireEnv("OCEL_STORAGE_COORDINATE");
  const coordinate = parseStorageCoordinate(value);
  if (coordinate === undefined) {
    throw new Error(
      `ocel cache: OCEL_STORAGE_COORDINATE ${JSON.stringify(value)} names no release`,
    );
  }
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

export const edgeReadableTagging = "ocel%3Aedge=readable";

export function edgeReadable(): boolean {
  return process.env.OCEL_ISR_EDGE_READABLE === "1";
}

export function providerObjectStore(): ObjectStore {
  return { bucket: requireEnv("OCEL_ISR_BUCKET"), client: newS3Client() };
}
