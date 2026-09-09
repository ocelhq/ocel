import type { ProviderDescriptor, VpsProviderOptions, VpsTarget } from "../../generated/config.js";

export type { VpsProviderOptions, VpsTarget };

/** Declares a VPS as the provider `ocel deploy` provisions into. */
export default function vpsProvider(options: VpsProviderOptions): ProviderDescriptor {
  return { name: "vps", options };
}
