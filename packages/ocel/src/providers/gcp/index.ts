import type { GcpProviderOptions, ProviderDescriptor } from "../../generated/config.js";

export type { GcpProviderOptions };

/**
 * Declares Google Cloud as the provider `ocel deploy` provisions into.
 *
 * Credentials are read from Application Default Credentials in the environment
 * the provider runs in: `gcloud auth application-default login`.
 */
export default function gcpProvider(options: GcpProviderOptions): ProviderDescriptor {
  return { name: "gcp", options };
}
