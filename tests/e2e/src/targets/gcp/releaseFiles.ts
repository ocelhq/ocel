import { bearer, type Where } from "./store";

const STORAGE = "https://storage.googleapis.com/storage/v1";
const STORAGE_COORDINATE_ENV = "OCEL_STORAGE_COORDINATE";
const PRODUCTION_EDGE_WORDS = "prod-edge";
const RANDOM_TAG = /^[0-9a-z]{6}$/;

type Listing = {
  services?: Array<{
    name?: string;
    template?: { containers?: Array<{ env?: Array<{ name?: string; value?: string }> }> };
  }>;
};

export function edgeBucketPrefix(namespace: string): string {
  return `${namespace}-${PRODUCTION_EDGE_WORDS}-`;
}

export function edgeBucketIn(buckets: string[], namespace: string): string {
  const prefix = edgeBucketPrefix(namespace);
  const found = buckets.filter(
    (name) => name.startsWith(prefix) && RANDOM_TAG.test(name.slice(prefix.length)),
  );
  if (found.length !== 1) {
    throw new Error(
      `want the one production edge bucket named ${prefix}<random tag>, found ${found.join(", ") || "none"}`,
    );
  }
  return found[0]!;
}

export function assetPrefixOf(body: unknown, service: string): string {
  const found = ((body as Listing).services ?? []).find(
    (each) => (each.name ?? "").split("/").pop() === service,
  );
  const coordinate = (found?.template?.containers ?? [])
    .flatMap((container) => container.env ?? [])
    .find((variable) => variable.name === STORAGE_COORDINATE_ENV)?.value;
  if (!coordinate) {
    throw new Error(
      `${service} is told no ${STORAGE_COORDINATE_ENV}, so nothing says where its release's files were uploaded`,
    );
  }
  return `assets/${coordinate}`;
}

export function releaseObjectUrl(bucket: string, assetPrefix: string, path: string): string {
  const name = `${assetPrefix}${path}`;
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
