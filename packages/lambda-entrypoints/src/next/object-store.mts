import { S3Client } from "@aws-sdk/client-s3";

export interface ObjectStore {
  client: S3Client;
  bucket: string;
}

const storeBucketEnv = "OCEL_ISR_STORE_BUCKET";

function env(name: string): string {
  const value = process.env[name];
  if (!value) throw new Error(`ocel cache handler: ${name} is not set`);
  return value;
}

export function entriesAdopted(): boolean {
  return Boolean(process.env[storeBucketEnv]);
}

export function providerObjectStore(): ObjectStore {
  return { bucket: env("OCEL_ISR_BUCKET"), client: new S3Client({}) };
}
