import type { ProviderDescriptor } from "ocel/config";

export interface AwsProviderOptions {
  region?: string;
  transforms?: readonly string[];
}

export default function awsProvider(
  options: AwsProviderOptions = {},
): ProviderDescriptor {
  return { package: "@ocel/provider-aws", options };
}
