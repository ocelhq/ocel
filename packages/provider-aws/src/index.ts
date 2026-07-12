import type { ProviderDescriptor } from "@ocel/sdk/config";

export type AwsProviderOptions = Record<string, unknown>;

/**
 * Declares AWS as the deploy target: `provider: awsProvider({ ... })` in
 * ocel.config.ts. Returns the descriptor the CLI reads to locate this
 * package's provider binary and the options it forwards unexamined.
 */
export default function awsProvider(
  options: AwsProviderOptions = {},
): ProviderDescriptor {
  return { package: "@ocel/provider-aws", options };
}
