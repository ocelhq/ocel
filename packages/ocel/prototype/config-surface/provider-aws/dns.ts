/** PROTOTYPE — `@ocel/provider-aws/dns` (ocelhq/ocel#399). */
import type { DnsDescriptor } from "ocel/config";

export interface Route53Options {
  /** Hosted zone id; default is the longest-suffix match over the account's zones. */
  zone?: string;
}

/** `ocel domain` writes records into Route 53 with the origin's credentials. */
export function route53(options: Route53Options = {}): DnsDescriptor {
  return { kind: "route53", ...options };
}
