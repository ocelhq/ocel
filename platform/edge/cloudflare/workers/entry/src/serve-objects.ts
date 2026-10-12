import type { AssetBucket, AssetObject } from "@framework/next-router/assets";
import type { PurgeMode } from "@platform/edge-contract/purge";
import type { EdgeObjectStore, EdgeStoredObject } from "./edge";
import type { PurgeOutcome } from "./purge-backlog";
import {
  buildObjectCall,
  buildServeRequest,
  isReleaseObjectKey,
  type ServeProps,
} from "./serve-key";

export type ServeFactory = (options: { props: ServeProps }) => {
  fetch(request: Request): Promise<Response>;
  purge(mode: PurgeMode, tags: string[]): Promise<PurgeOutcome>;
};

export type ServedObject = EdgeStoredObject & AssetObject;

export function serveObjects(
  serve: ServeFactory,
  host: string,
): EdgeObjectStore & AssetBucket & { get(key: string): Promise<ServedObject | null> } {
  return {
    async get(key) {
      if (!isReleaseObjectKey(key)) return null;
      const { url, props } = buildObjectCall(host, key);
      const res = await serve({ props }).fetch(buildServeRequest(url));
      if (res.status === 404) {
        await res.body?.cancel();
        return null;
      }
      if (!res.ok) {
        await res.body?.cancel();
        throw new Error(`ocel: reading ${key} through Serve answered ${res.status}`);
      }
      return {
        body: res.body,
        httpEtag: res.headers.get("etag") ?? undefined,
        text: () => res.text(),
        arrayBuffer: () => res.arrayBuffer(),
      };
    },
  };
}
