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

// Whether this substrate's edge offered a cache store, which is what decides
// where route entries live. It reads only the bucket name, never the injected
// credentials — route entries no longer touch them at all, since both halves of
// their traffic go through the ISR writer worker.
export function entriesAdopted(): boolean {
  return Boolean(process.env[storeBucketEnv]);
}

// THE LAST STANDING R2 CREDENTIAL. Its one remaining caller is the tag-clock
// snapshot publisher, which still writes tag-clock.json into the adopted store
// directly; epic decision 8 routes every runtime snapshot writer through the
// Durable Object, and when it does (ocelhq-wvag.4) this function, the two key
// env vars it reads, and the whole injection behind them go with it.
//
// Until then the deployed function holds an R2 token that can write any object
// in the shared bucket, because R2 tokens scope to a bucket and have no
// key-prefix grammar. Nothing else here may start using it.
export function snapshotObjectStore(): ObjectStore | null {
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
