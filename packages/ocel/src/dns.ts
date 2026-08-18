/** Options for Cloudflare DNS, authored inline in `ocel.config.ts`. */
export interface CloudflareDnsOptions {
  /**
   * The zone the records are written into. Omit it and ocel picks the zone
   * that covers the hostname.
   */
  zone?: string;
}

/** Declares Cloudflare as the DNS the project's records are written into. */
export function cloudflareDns(options: CloudflareDnsOptions = {}): {
  kind: "cloudflare";
  zone?: string;
} {
  return { kind: "cloudflare", ...options };
}
