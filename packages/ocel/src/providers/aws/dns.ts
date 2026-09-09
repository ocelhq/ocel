import type { DnsDescriptor } from "../../generated/config.js";

/** Options for Route 53. */
export interface Route53Options {
  /**
   * The hosted zone the records are written into. Omit it and ocel picks the
   * zone that covers the hostname.
   */
  zone?: string;
}

/** Declares Route 53 as the DNS the project's records are written into. */
export function route53(options: Route53Options = {}): DnsDescriptor {
  return { kind: "route53", ...options };
}
