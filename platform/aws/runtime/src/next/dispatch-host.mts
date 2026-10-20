import {
  type DispatchAccess,
  newDispatchInvoke,
  readDispatchHost,
} from "@framework/next-runtime/dispatch-host";
import type { Invoke } from "@framework/node-runtime/host";
import { s3AssetBucket } from "./dispatch-assets.mjs";
import { credentialsOf, s3ObjectFetch, siblingOriginFetch } from "./dispatch-signing.mjs";
import { isLambdaThrottle } from "./lambda-throttle.mjs";
import { edgeBucketVar } from "./object-store.mjs";

const lambdaInvokePayloadBytes = 6 * 1024 * 1024;

const functionUrlEventEnvelopeBytes = 64 * 1024;

export function awsDispatchAccess(env: NodeJS.ProcessEnv): DispatchAccess {
  const region = env.AWS_REGION;
  const bucket = env[edgeBucketVar];
  if (bucket && !(region && credentialsOf(env))) {
    throw new Error(
      `ocel: ${edgeBucketVar} names ${bucket} but this function has no credentials to read it with`,
    );
  }
  return {
    ...(bucket && region
      ? { assetBucket: s3AssetBucket(bucket, region, s3ObjectFetch(env, region)) }
      : {}),
    originFetch: siblingOriginFetch(env, region),
    isOriginThrottle: isLambdaThrottle,
    maxImageRequestBytes: lambdaInvokePayloadBytes - functionUrlEventEnvelopeBytes,
  };
}

export function newAwsDispatchInvoke(localOrigin: string): Invoke {
  return newDispatchInvoke(
    readDispatchHost(process.env, localOrigin, awsDispatchAccess(process.env)),
  );
}
