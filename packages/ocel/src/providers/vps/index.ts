import type {
  ProviderDescriptor,
  VpsProviderOptions,
  VpsProxy,
  VpsTarget,
} from "../../generated/config.js";

export type { VpsProviderOptions, VpsProxy, VpsTarget };

/** Declares a VPS as the provider `ocel deploy` provisions into. */
export default function vpsProvider(options: VpsProviderOptions): ProviderDescriptor {
  return { vps: options };
}
