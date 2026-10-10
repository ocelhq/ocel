import type { ReleasesBinding } from "./releases";

export interface IsrWriterBinding {
  fetch(request: Request): Promise<Response>;
}

export interface CacheEntrypointProps {
  isrWriteSecret?: string;
  scope?: string;
}

export interface Env {
  RELEASES: ReleasesBinding;
  OCEL_SLUG: string;
  OCEL_APP?: string;
  OCEL_DOMAIN_APPS?: string;
  OCEL_PREVIEW?: string;
  OCEL_PREVIEW_GLOBAL?: string;
  OCEL_PREVIEW_BASE_DOMAIN?: string;
  OCEL_PREVIEW_KEY?: string;
  OCEL_CACHE_STORE?: R2Bucket;
  ISR_WRITER?: IsrWriterBinding;
  OCEL_EDGE_ACCESS_KEY_ID?: string;
  OCEL_EDGE_SECRET_KEY?: string;
  OCEL_ASSET_STORE_BUCKET?: string;
  OCEL_ASSET_STORE_PREFIX?: string;
  OCEL_ASSET_STORE_ACCESS_KEY_ID?: string;
  OCEL_ASSET_STORE_SECRET_KEY?: string;
  OCEL_ORIGIN_CLIENT_CERTIFICATE?: Fetcher;
  OCEL_ENVELOPE_KEY?: string;
  OCEL_REVALIDATE_QUEUE_URL?: string;
  OCEL_REFRESH_QUEUE?: Queue;
  OCEL_IMAGE_OPTIMIZER_URL?: string;
  LOADER?: WorkerLoader;
}
