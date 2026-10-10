import type { AssetBucket, AssetObject } from "@framework/next-router/assets";
import type { EdgeObjectStore, EdgeStoredObject } from "./edge";
import { objectCall, type ServeProps } from "./serve-key";

export type ServeFactory = (options: { props: ServeProps }) => {
  fetch(request: Request): Promise<Response>;
};

export type ServedObject = EdgeStoredObject & AssetObject;

export function serveObjects(
  serve: ServeFactory,
  host: string,
): EdgeObjectStore & AssetBucket & { get(key: string): Promise<ServedObject | null> } {
  return {
    async get(key) {
      const { url, props } = objectCall(host, key);
      const res = await serve({ props }).fetch(new Request(url));
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
