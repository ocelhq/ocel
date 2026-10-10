import type { AssetBucket } from "@framework/next-router/assets";
import { AwsClient } from "aws4fetch";
import type { Env } from "./env";

export interface HmacAssetStore {
  endpoint: string;
  accessKeyId: string;
  secretAccessKey: string;
  bucket: string;
  prefix: string;
}

const readAttempts = 3;

export function newHmacAssetBucket(store: HmacAssetStore): AssetBucket {
  const origin = store.endpoint.replace(/\/$/, "");
  const client = new AwsClient({
    accessKeyId: store.accessKeyId,
    secretAccessKey: store.secretAccessKey,
    service: "s3",
    region: "auto",
    retries: readAttempts - 1,
  });
  return {
    async get(key) {
      const name = `${store.prefix}${key}`;
      const path = name.split("/").map(encodeURIComponent).join("/");
      const response = await client.fetch(`${origin}/${store.bucket}/${path}`);
      if (response.status === 404) {
        await response.body?.cancel();
        return null;
      }
      if (!response.ok) {
        await response.body?.cancel();
        throw new Error(
          `ocel: the asset bucket ${store.bucket} answered ${response.status} for ${name}`,
        );
      }
      return { body: response.body, httpEtag: response.headers.get("etag") ?? undefined };
    },
  };
}

export function readHmacAssetBucket(
  env: Pick<
    Env,
    | "OCEL_ASSET_STORE_ENDPOINT"
    | "OCEL_ASSET_STORE_BUCKET"
    | "OCEL_ASSET_STORE_PREFIX"
    | "OCEL_ASSET_STORE_ACCESS_KEY_ID"
    | "OCEL_ASSET_STORE_SECRET_KEY"
  >,
): AssetBucket | undefined {
  const endpoint = env.OCEL_ASSET_STORE_ENDPOINT;
  const bucket = env.OCEL_ASSET_STORE_BUCKET;
  const accessKeyId = env.OCEL_ASSET_STORE_ACCESS_KEY_ID;
  const secretAccessKey = env.OCEL_ASSET_STORE_SECRET_KEY;
  if (!endpoint || !bucket || !accessKeyId || !secretAccessKey) return undefined;
  return newHmacAssetBucket({
    endpoint,
    bucket,
    accessKeyId,
    secretAccessKey,
    prefix: env.OCEL_ASSET_STORE_PREFIX ?? "",
  });
}
