/** PROTOTYPE — `ocel/dns` (ocelhq/ocel#399). */
import type { DnsDescriptor } from "ocel/config";

export interface CloudflareDnsOptions {
  /** Cloudflare zone id; default is the longest-suffix match over the token's zones. */
  zone?: string;
}

/** `ocel domain` writes records into a Cloudflare zone with `CLOUDFLARE_API_TOKEN`. */
export function cloudflareDns(options: CloudflareDnsOptions = {}): DnsDescriptor {
  return { kind: "cloudflare", ...options };
}
