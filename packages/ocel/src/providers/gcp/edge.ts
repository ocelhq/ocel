import type { AlbEdgeOptions } from "../../generated/config.js";

export type { AlbEdgeOptions } from "../../generated/config.js";

/**
 * Declares a global external Application Load Balancer, with Cloud CDN in
 * front of Cloud Run, as the edge the project's hostnames are served from.
 *
 * One load balancer runs per bootstrap tier and every project in that tier
 * is answered by it. It is the only piece of GCP infrastructure ocel provisions
 * that costs money while it serves no traffic — roughly $18 a month per tier
 * plus Premium-tier egress — so it is never the default: a project that names
 * no edge is answered on the URL Cloud Run gives each service, and binds no
 * hostname.
 */
export function alb(options: AlbEdgeOptions = {}): { alb: AlbEdgeOptions } {
  return { alb: options };
}
