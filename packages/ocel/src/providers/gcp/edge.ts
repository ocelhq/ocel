import type { EdgeDescriptor } from "../../generated/config.js";

/**
 * Options for the Application Load Balancer edge.
 *
 * Empty at release: everything the load balancer needs comes from the
 * provider's own options, so a later option lands without a signature change.
 */
export type AlbEdgeOptions = Record<string, never>;

/**
 * Declares a global external Application Load Balancer, with Cloud CDN in
 * front of Cloud Run, as the edge the project's hostnames are served from.
 *
 * One load balancer stands per bootstrap class and every project in that class
 * is answered by it. It is the only piece of GCP infrastructure ocel stands up
 * that costs money while it serves no traffic — roughly $18 a month per class
 * plus Premium-tier egress — so it is never the default: a project that names
 * no edge is answered on the URL Cloud Run gives each service, and that edge
 * binds no hostname.
 */
export function alb(_options: AlbEdgeOptions = {}): EdgeDescriptor {
  return { kind: "alb" };
}
