import type { ProviderDescriptor } from "ocel/config";

/** Options for the AWS provider, authored inline in `ocel.config.ts`. */
export interface AwsProviderOptions {
  /** The AWS region to deploy into. */
  region?: string;
  /**
   * Transform modules to apply while provisioning, in order — later modules
   * win where their patches collide. Each is a path to a module whose default
   * export is a `defineTransform(...)` result. Omit this and ocel provisions
   * exactly as it does without transforms.
   */
  transforms?: readonly string[];
  /**
   * Certificates to serve a hostname with, keyed by hostname, valued by the
   * ARN of an already-issued ACM certificate. Optional: a hostname listed here
   * is served with the certificate you name, and one that is not gets an ACM
   * certificate ocel requests, validates through DNS and deletes again once
   * nothing it serves is left. A certificate you pin is never requested,
   * renewed or deleted here.
   */
  certificates?: Record<string, string>;
}

/** Declares AWS as the provider `ocel deploy` provisions into. */
export default function awsProvider(
  options: AwsProviderOptions = {},
): ProviderDescriptor {
  return { package: "@ocel/provider-aws", options };
}
