import { S3Client } from "@aws-sdk/client-s3";

// A bucket and the client that addresses it.
export interface ObjectStore {
  client: S3Client;
  bucket: string;
}

// The whole of what the deploy tells a function about this substrate's adopted
// cache store. It is a name, not a credential: nothing here signs a request
// against that bucket, so there is nothing else to inject.
const storeBucketEnv = "OCEL_ISR_STORE_BUCKET";

function env(name: string): string {
  const value = process.env[name];
  if (!value) throw new Error(`ocel cache handler: ${name} is not set`);
  return value;
}

// Whether this substrate's edge offered a cache store, which is what decides
// where route entries live. It reads a name and nothing more: route entries
// travel through the ISR writer worker in both directions, and the tag clock is
// published off the state table's stream, so a deployed function holds no R2
// credential at all — which is the point, since an R2 token scopes to a bucket
// and nothing finer, and that bucket is shared by every project on the
// substrate. Nothing here may start injecting one again.
export function entriesAdopted(): boolean {
  return Boolean(process.env[storeBucketEnv]);
}

// providerObjectStore is the account's own bucket under the function's own role.
// Route entries land here only when no store was adopted; fetch entries land
// here always, since their bodies are origin-private and must not replicate to
// an edge.
export function providerObjectStore(): ObjectStore {
  return { bucket: env("OCEL_ISR_BUCKET"), client: new S3Client({}) };
}
