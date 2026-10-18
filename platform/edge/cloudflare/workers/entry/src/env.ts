import type { ReleasesBinding } from "./releases";

export interface Env {
  RELEASES: ReleasesBinding;
  OCEL_SLUG: string;
  OCEL_APP?: string;
  OCEL_SERVES_WORKERS_DEV?: string;
  OCEL_DOMAIN_APPS?: string;
  OCEL_PREVIEW?: string;
  OCEL_PREVIEW_GLOBAL?: string;
  OCEL_PREVIEW_BASE_DOMAIN?: string;
  OCEL_PREVIEW_KEY?: string;
  OCEL_ORIGIN: string;
  OCEL_ORIGIN_CLIENT_CERTIFICATE?: Fetcher;
  OCEL_ENVELOPE_KEY?: string;
  OCEL_PURGE_BUDGET?: string;
  LOADER: WorkerLoader;
}
