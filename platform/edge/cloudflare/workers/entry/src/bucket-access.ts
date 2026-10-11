import { AwsClient } from "aws4fetch";
import type { Env } from "./env";

export interface BucketAccess {
  bucket: string;
  origin: string;
  prefix: string;
  region: string;
  accessKeyId: string;
  secretAccessKey: string;
}

function findAwsBucketAccess(env: Env): BucketAccess | undefined {
  const {
    OCEL_ASSET_BUCKET: bucket,
    OCEL_AWS_REGION: region,
    OCEL_EDGE_ACCESS_KEY_ID: accessKeyId,
    OCEL_EDGE_SECRET_KEY: secretAccessKey,
  } = env;
  if (!bucket || !region || !accessKeyId || !secretAccessKey) return undefined;
  const origin = `https://${bucket}.s3.${region}.amazonaws.com`;
  return { bucket, origin, prefix: "", region, accessKeyId, secretAccessKey };
}

function findHmacBucketAccess(env: Env): BucketAccess | undefined {
  const {
    OCEL_ASSET_STORE_ENDPOINT: endpoint,
    OCEL_ASSET_STORE_BUCKET: bucket,
    OCEL_ASSET_STORE_ACCESS_KEY_ID: accessKeyId,
    OCEL_ASSET_STORE_SECRET_KEY: secretAccessKey,
  } = env;
  if (!endpoint || !bucket || !accessKeyId || !secretAccessKey) return undefined;
  const origin = `${endpoint.replace(/\/$/, "")}/${encodeURIComponent(bucket)}`;
  const prefix = env.OCEL_ASSET_STORE_PREFIX ?? "";
  return { bucket, origin, prefix, region: "auto", accessKeyId, secretAccessKey };
}

export function findBucketAccess(env: Env): BucketAccess | undefined {
  return findAwsBucketAccess(env) ?? findHmacBucketAccess(env);
}

export function signBucket(access: BucketAccess): AwsClient {
  const { region, accessKeyId, secretAccessKey } = access;
  return new AwsClient({ accessKeyId, secretAccessKey, service: "s3", region, retries: 0 });
}
