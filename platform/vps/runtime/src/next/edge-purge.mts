import { newEdgePurge } from "@framework/node-runtime/edge-purge";
import type { PurgeTags } from "@framework/node-runtime/tag-records";
import { readStorageCoordinate } from "@platform/edge-contract/runtime-env";

export function openEdgePurge(env: NodeJS.ProcessEnv): PurgeTags | undefined {
  const url = env.OCEL_PURGE_URL;
  if (!url) return undefined;
  const secret = env.OCEL_PURGE_SECRET;
  const coordinate = readStorageCoordinate(env);
  if (!secret || coordinate === undefined) {
    throw new Error(
      "ocel: OCEL_PURGE_URL names where to purge, and this container is told no OCEL_PURGE_SECRET or storage coordinate to sign the purge with",
    );
  }
  return newEdgePurge({ url, coordinate, secret });
}
