import { S3Client } from "@aws-sdk/client-s3";

// A bucket and the client that addresses it.
export interface ObjectStore {
  client: S3Client;
  bucket: string;
}

// The coordinates the membrane injects into node when this substrate's edge
// offered a cache store. All five are written together or none is, so the
// bucket alone decides whether a store was adopted and the rest are then
// required — a partial injection is a bug worth failing on, not falling back on.
const storeBucketEnv = "OCEL_ISR_STORE_BUCKET";
const storeEndpointEnv = "OCEL_ISR_STORE_ENDPOINT";
const storeRegionEnv = "OCEL_ISR_STORE_REGION";
const storeAccessKeyEnv = "OCEL_ISR_STORE_ACCESS_KEY_ID";
const storeSecretEnv = "OCEL_ISR_STORE_SECRET_ACCESS_KEY";

function env(name: string): string {
  const value = process.env[name];
  if (!value) throw new Error(`ocel cache handler: ${name} is not set`);
  return value;
}

// adoptedObjectStore is the edge-provisioned store the membrane injected, or
// null when this substrate adopted none. It is the store the edge itself reads
// through a native binding; the origin reaches the same objects over the
// S3-compatible API, so one client type serves both sides.
export function adoptedObjectStore(): ObjectStore | null {
  if (!process.env[storeBucketEnv]) return null;
  return {
    bucket: env(storeBucketEnv),
    client: new S3Client({
      region: env(storeRegionEnv),
      endpoint: env(storeEndpointEnv),
      credentials: {
        accessKeyId: env(storeAccessKeyEnv),
        secretAccessKey: env(storeSecretEnv),
      },
    }),
  };
}

// providerObjectStore is the account's own bucket under the function's own role.
// Route entries land here only when no store was adopted; fetch entries land
// here always, since their bodies are origin-private and must not replicate to
// an edge.
export function providerObjectStore(): ObjectStore {
  return { bucket: env("OCEL_ISR_BUCKET"), client: new S3Client({}) };
}

// isrObjectStore is where the singular cache handler's entries live: the adopted
// store when there is one, so the edge reads them without leaving its own
// network, and the provider's own bucket when there is not. Both sides address
// identical keys, so an un-adopted substrate is a rollback and not a migration.
export function isrObjectStore(): ObjectStore {
  return adoptedObjectStore() ?? providerObjectStore();
}
