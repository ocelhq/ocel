import { S3Client } from "@aws-sdk/client-s3";

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

export function providerObjectStore(): ObjectStore {
  return { bucket: requireEnv("OCEL_ISR_BUCKET"), client: new S3Client({}) };
}
