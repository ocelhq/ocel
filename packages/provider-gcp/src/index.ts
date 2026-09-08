import type { ProviderDescriptor } from "ocel/config";

/** Options for the Google Cloud provider, authored inline in `ocel.config.ts`. */
export interface GcpProviderOptions {
  /** The Google Cloud project to deploy into. */
  project: string;
  /** The region to deploy into. A project spans them all, so this names the one. */
  region: string;
}

/**
 * Declares Google Cloud as the provider `ocel deploy` provisions into.
 *
 * Credentials are read from Application Default Credentials in the environment
 * the provider runs in: `gcloud auth application-default login`.
 */
export default function gcpProvider(options: GcpProviderOptions): ProviderDescriptor {
  return { package: "@ocel/provider-gcp", options };
}
