import type { CloudflareEdgeOptions, EdgeDescriptor } from "./generated/config.js";

export type { CloudflareEdgeOptions } from "./generated/config.js";

/** Declares Cloudflare as the edge the project's hostnames are served from. */
export function cloudflare(options: CloudflareEdgeOptions = {}): EdgeDescriptor {
  return { cloudflare: options };
}
