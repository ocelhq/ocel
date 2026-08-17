/** PROTOTYPE — `@ocel/provider-aws` (ocelhq/ocel#399). */
import type { ProviderDescriptor } from "ocel/config";

export interface AwsProviderOptions {
  region?: string;
  transforms?: readonly string[];
  /**
   * Pinned certificates by hostname (wildcards allowed), ARN each. Verified
   * — ISSUED, right region, covers the host — never issued or deleted by
   * ocel. Absent ⇒ lookup by name in ACM, else `ocel domain add` requests one.
   */
  certificates?: Readonly<Record<string, string>>;
}

/**
 * No option here depends on the edge mode: `exposeFunctionURLs` is gone with
 * the Function URL front, so nothing needs "only when edge is off" typing.
 */
export default function awsProvider(options: AwsProviderOptions = {}): ProviderDescriptor {
  return { package: "@ocel/provider-aws", options };
}
