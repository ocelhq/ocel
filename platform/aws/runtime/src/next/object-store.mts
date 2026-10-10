import type { S3Client } from "@aws-sdk/client-s3";
import { type IsrWriterClient, newIsrWriterClient } from "@platform/edge-contract/isr-writer";
import { newS3Client } from "./sdk-clients.mjs";

export interface ObjectStore {
  client: S3Client;
  bucket: string;
}

const storeBucketEnv = "OCEL_ISR_STORE_BUCKET";

export function requireEnv(name: string): string {
  const value = process.env[name];
  if (!value) throw new Error(`ocel cache: ${name} is not set`);
  return value;
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

export function entriesAdopted(): boolean {
  return Boolean(process.env[storeBucketEnv]);
}

export function openAdoptedIsrWriter(isrPrefix: string): IsrWriterClient {
  const endpoint = process.env.OCEL_ISR_WRITER_URL;
  const secret = process.env.OCEL_ISR_WRITER_SECRET;
  if (!endpoint || !secret) {
    throw new Error(
      "ocel cache handler: OCEL_ISR_WRITER_URL and OCEL_ISR_WRITER_SECRET must both be set " +
        "when this deploy reads its ISR entries from an adopted cache store; " +
        "re-run `ocel bootstrap production` and redeploy",
    );
  }
  return newIsrWriterClient({ endpoint, isrPrefix, secret });
}

export function providerObjectStore(): ObjectStore {
  return { bucket: requireEnv("OCEL_ISR_BUCKET"), client: newS3Client() };
}
