import type { NextHost } from "@framework/next-runtime/host";
import { instanceCacheBytes, newInstanceCache } from "@framework/next-runtime/instance-cache";
import { newS3Client } from "../cache/sdk-clients.mjs";
import { newAwsContainerNextHost } from "./container-next-host.mjs";
import { newAwsDispatchInvoke } from "./dispatch-host.mjs";
import type { CredentialSource } from "./dispatch-signing.mjs";

export function taskCredentials(): CredentialSource {
  const resolve = newS3Client().config.credentials;
  return async () => {
    const held = await resolve();
    return {
      accessKeyId: held.accessKeyId,
      secretAccessKey: held.secretAccessKey,
      sessionToken: held.sessionToken,
    };
  };
}

export function newAwsFunctionContainerHost(
  env: NodeJS.ProcessEnv,
  credentials: CredentialSource = taskCredentials(),
): NextHost {
  return {
    ...newAwsContainerNextHost(),
    newDispatchInvoke: async (localOrigin) => newAwsDispatchInvoke(localOrigin, env, credentials),
    instanceCache: newInstanceCache(instanceCacheBytes()),
  };
}
