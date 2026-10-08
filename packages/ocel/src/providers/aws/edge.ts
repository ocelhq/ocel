import type { ApiGatewayEdgeOptions, CloudFrontEdgeOptions } from "../../generated/config.js";

export type { ApiGatewayEdgeOptions, CloudFrontEdgeOptions } from "../../generated/config.js";

/** Declares CloudFront as the edge the project's hostnames are served from. */
export function cloudfront(options: CloudFrontEdgeOptions = {}): {
  cloudfront: CloudFrontEdgeOptions;
} {
  return { cloudfront: options };
}

/** Declares API Gateway as the edge the project's hostnames are served from. */
export function apiGateway(options: ApiGatewayEdgeOptions = {}): {
  "api-gateway": ApiGatewayEdgeOptions;
} {
  return { "api-gateway": options };
}
