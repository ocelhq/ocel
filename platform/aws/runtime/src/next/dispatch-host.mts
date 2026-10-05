import {
  type DispatchAccess,
  newDispatchInvoke,
  readDispatchHost,
} from "@framework/next-runtime/dispatch-host";
import type { Invoke } from "@framework/node-runtime/host";
import { s3AssetBucket } from "./dispatch-assets.mjs";
import { credentialsOf, s3ObjectFetch, siblingOriginFetch } from "./dispatch-signing.mjs";

const assetBucketVar = "OCEL_ASSET_BUCKET";

export function awsDispatchAccess(env: NodeJS.ProcessEnv): DispatchAccess {
  const region = env.AWS_REGION;
  const bucket = env[assetBucketVar];
  if (bucket && !(region && credentialsOf(env))) {
    throw new Error(
      `ocel: ${assetBucketVar} names ${bucket} but this function has no credentials to read it with`,
    );
  }
  return {
    ...(bucket && region
      ? { assetBucket: s3AssetBucket(bucket, region, s3ObjectFetch(env, region)) }
      : {}),
    originFetch: siblingOriginFetch(env, region),
  };
}

export function newAwsDispatchInvoke(localOrigin: string): Invoke {
  return newDispatchInvoke(
    readDispatchHost(process.env, localOrigin, awsDispatchAccess(process.env)),
  );
}
