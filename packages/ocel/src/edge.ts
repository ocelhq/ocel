import type { EdgeDescriptor } from "./config.js";

/**
 * Options for the Cloudflare edge. The token and account id are read from the
 * environment.
 */
export type CloudflareEdgeOptions = {
  /**
   * Reach the origin through a Cloudflare Tunnel the origin opens, rather than at
   * its address. Only a VPS box runs one; other providers refuse it.
   */
  tunnel?: boolean;
};

/** Declares Cloudflare as the edge the project's hostnames are served from. */
export function cloudflare(options: CloudflareEdgeOptions = {}): EdgeDescriptor {
  return { cloudflare: options };
}
