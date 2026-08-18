import type { EdgeDescriptor } from "./config.js";

/**
 * Options for the Cloudflare edge, authored inline in `ocel.config.ts`.
 *
 * Empty at release: the token and account id are read from the environment,
 * so a later option lands without a signature change.
 */
export type CfEdgeOptions = Record<string, never>;

/** Declares Cloudflare as the edge the project's hostnames are served from. */
export function cfEdge(options: CfEdgeOptions = {}): EdgeDescriptor {
  return { kind: "cloudflare", options };
}
