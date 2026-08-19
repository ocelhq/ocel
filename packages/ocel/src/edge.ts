import type { EdgeDescriptor } from "./config.js";

/**
 * Options for the Cloudflare edge, authored inline in `ocel.config.ts`.
 *
 * Empty at release: the token and account id are read from the environment,
 * so a later option lands without a signature change.
 */
export type CloudflareEdgeOptions = Record<string, never>;

/** Declares Cloudflare as the edge the project's hostnames are served from. */
export function cloudflare(options: CloudflareEdgeOptions = {}): EdgeDescriptor {
  return { kind: "cloudflare", options } as unknown as EdgeDescriptor;
}
