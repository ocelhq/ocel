import type { ProviderDescriptor } from "ocel/config";

export type AwsProviderOptions = Record<string, unknown>;

export default function awsProvider(
  options: AwsProviderOptions = {},
): ProviderDescriptor {
  return { package: "@ocel/provider-aws", options };
}
