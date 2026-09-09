import type { DnsDescriptor } from "./config.js";

/** Options for Cloudflare DNS. */
export interface CloudflareDnsOptions {
  /**
   * The zone the records are written into. Omit it and ocel picks the zone
   * that covers the hostname.
   */
  zone?: string;
}

/** Declares Cloudflare as the DNS the project's records are written into. */
export function cloudflareDns(options: CloudflareDnsOptions = {}): DnsDescriptor {
  return { kind: "cloudflare", ...options };
}
