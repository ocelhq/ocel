import {
  type DispatchAccess,
  type DispatchHost,
  dispatchRequest,
  readDispatchHost,
} from "@framework/next-runtime/dispatch-host";
import { fetchToNodeHandler } from "@framework/node-runtime/fetch-bridge";
import type { Invoke } from "@framework/node-runtime/host";
import { bucketVars } from "@platform/edge-contract/runtime-env";
import { s3AssetBucket } from "./dispatch-assets.mjs";
import {
  type CredentialSource,
  credentialsOf,
  envCredentials,
  s3ObjectFetch,
  signedOriginFetch,
} from "./dispatch-signing.mjs";
import { isLambdaThrottle } from "./lambda-throttle.mjs";

const lambdaInvokePayloadBytes = 6 * 1024 * 1024;

const functionUrlEventEnvelopeBytes = 64 * 1024;

export function awsDispatchAccess(
  env: NodeJS.ProcessEnv,
  credentials?: CredentialSource,
): DispatchAccess {
  const region = env.AWS_REGION;
  const bucket = env[bucketVars.edge];
  if (bucket && !(region && (credentials || credentialsOf(env)))) {
    throw new Error(
      `ocel: ${bucketVars.edge} names ${bucket} but this function has no credentials to read it with`,
    );
  }
  const signing = credentials ?? envCredentials(env);
  return {
    ...(bucket && region
      ? { assetBucket: s3AssetBucket(bucket, region, s3ObjectFetch(signing, region)) }
      : {}),
    originFetch: signedOriginFetch(signing, region),
    isOriginThrottle: isLambdaThrottle,
    maxImageRequestBytes: lambdaInvokePayloadBytes - functionUrlEventEnvelopeBytes,
  };
}

const cloudFrontRouterKind = "cloudfront";

const originVaryHeader = "x-ocel-vary";

function carryVary(response: Response): Response {
  const vary = response.headers.get("vary");
  if (vary === null) return response;
  const carried = new Response(response.body, response);
  carried.headers.delete("vary");
  carried.headers.set(originVaryHeader, vary);
  return carried;
}

export async function dispatchForAws(
  request: Request,
  host: DispatchHost,
  waitUntil: (promise: Promise<unknown>) => void,
): Promise<Response> {
  const response = await dispatchRequest(request, host, waitUntil);
  return host.routerKind === cloudFrontRouterKind ? carryVary(response) : response;
}

export function newAwsDispatchInvoke(
  localOrigin: string,
  env: NodeJS.ProcessEnv = process.env,
  credentials?: CredentialSource,
): Invoke {
  const host = readDispatchHost(env, localOrigin, awsDispatchAccess(env, credentials));
  return (req, res, ocel) =>
    fetchToNodeHandler((request) => dispatchForAws(request, host, ocel.waitUntil))(req, res, ocel);
}
