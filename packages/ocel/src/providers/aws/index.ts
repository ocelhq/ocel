import type { AwsProviderOptions, ProviderDescriptor } from "../../generated/config.js";

export type { AwsProviderOptions };

/** Declares AWS as the provider `ocel deploy` provisions into. */
export default function awsProvider(options: AwsProviderOptions = {}): ProviderDescriptor {
  return { name: "aws", options };
}
