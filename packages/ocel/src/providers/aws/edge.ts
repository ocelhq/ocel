import type { EdgeDescriptor } from "../../generated/config.js";

/**
 * Options for the CloudFront edge.
 *
 * Empty at release: everything CloudFront needs comes from the provider's own
 * options, so a later option lands without a signature change.
 */
export type CloudFrontEdgeOptions = Record<string, never>;

/** Declares CloudFront as the edge the project's hostnames are served from. */
export function cloudfront(options: CloudFrontEdgeOptions = {}): EdgeDescriptor {
  return { cloudfront: options };
}

/**
 * Options for the API Gateway edge.
 *
 * Empty at release: everything API Gateway needs comes from the provider's own
 * options, so a later option lands without a signature change.
 */
export type ApiGatewayEdgeOptions = Record<string, never>;

/** Declares API Gateway as the edge the project's hostnames are served from. */
export function apiGateway(options: ApiGatewayEdgeOptions = {}): EdgeDescriptor {
  return { "api-gateway": options };
}
