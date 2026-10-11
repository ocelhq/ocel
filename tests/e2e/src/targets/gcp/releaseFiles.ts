import { PRODUCTION_TIER } from "./names";
import { bearer, type Where } from "./store";

const STORAGE = "https://storage.googleapis.com/storage/v1";
const ASSET_PREFIX_ENV = "OCEL_ASSET_PREFIX";
const ASSETS_STORE = "assets";

type Listing = {
  services?: Array<{
    name?: string;
    template?: { containers?: Array<{ env?: Array<{ name?: string; value?: string }> }> };
  }>;
};

export function tierBucket(namespace: string, project: string): string {
  return `${namespace}-${project}-${PRODUCTION_TIER}`;
}

export function assetPrefixOf(body: unknown, service: string): string {
  const found = ((body as Listing).services ?? []).find(
    (each) => (each.name ?? "").split("/").pop() === service,
  );
  const prefix = (found?.template?.containers ?? [])
    .flatMap((container) => container.env ?? [])
    .find((variable) => variable.name === ASSET_PREFIX_ENV)?.value;
  if (!prefix) {
    throw new Error(
      `${service} is told no ${ASSET_PREFIX_ENV}, so nothing says where its release's files were uploaded`,
    );
  }
  return prefix;
}

export function releaseObjectUrl(bucket: string, assetPrefix: string, path: string): string {
  const name = `${ASSETS_STORE}/${assetPrefix}${path}`;
  return `${STORAGE}/b/${bucket}/o/${encodeURIComponent(name)}?alt=media`;
}

export async function readReleaseFile(
  where: Where,
  bucket: string,
  assetPrefix: string,
  path: string,
): Promise<Uint8Array> {
  const at = releaseObjectUrl(bucket, assetPrefix, path);
  const answered = await fetch(at, { headers: bearer(where) });
  if (!answered.ok) {
    throw new Error(`GET ${at} = ${answered.status} ${await answered.text()}`);
  }
  return new Uint8Array(await answered.arrayBuffer());
}
