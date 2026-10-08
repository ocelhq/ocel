import type { CloudflareEdgeOptions } from "./generated/config.js";

export type { CloudflareEdgeOptions } from "./generated/config.js";

/** Declares Cloudflare as the edge the project's hostnames are served from. */
export function cloudflare(options: CloudflareEdgeOptions = {}): {
  cloudflare: CloudflareEdgeOptions;
} {
  return { cloudflare: options };
}
