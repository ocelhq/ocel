import type { DeploymentsBinding } from "./deployments";

export interface IsrWriterBinding {
  fetch(request: Request): Promise<Response>;
}

export interface CacheEntrypointProps {
  isrWriteSecret?: string;
}

export interface Env {
  DEPLOYMENTS: DeploymentsBinding;
  OCEL_SLUG: string;
  OCEL_APP?: string;
  OCEL_PREVIEW?: string;
  OCEL_PREVIEW_GLOBAL?: string;
  OCEL_PREVIEW_BASE_DOMAIN?: string;
  OCEL_PREVIEW_APPS?: string;
  OCEL_CACHE_STORE?: R2Bucket;
  ISR_WRITER?: IsrWriterBinding;
  OCEL_EDGE_ACCESS_KEY_ID?: string;
  OCEL_EDGE_SECRET_KEY?: string;
  OCEL_AWS_REGION?: string;
  OCEL_REVALIDATE_QUEUE_URL?: string;
  OCEL_STATE_TABLE?: string;
  OCEL_ISR_BUCKET?: string;
  OCEL_IMAGE_OPTIMIZER_URL?: string;
  OCEL_ORIGIN_BODY_LIMIT?: string;
  OCEL_ORIGIN_BODY_ENCODING?: string;
  LOADER?: WorkerLoader;
}
