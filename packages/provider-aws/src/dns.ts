/** Options for Route 53, authored inline in `ocel.config.ts`. */
export interface Route53Options {
  /**
   * The hosted zone the records are written into. Omit it and ocel picks the
   * zone that covers the hostname.
   */
  zone?: string;
}

/** Declares Route 53 as the DNS the project's records are written into. */
export function route53(options: Route53Options = {}): {
  kind: "route53";
  zone?: string;
} {
  return { kind: "route53", ...options };
}
