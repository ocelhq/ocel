import type {
  ApiGatewayEdgeOptions,
  CloudFrontEdgeOptions,
  EdgeDescriptor,
} from "../../generated/config.js";

export type { ApiGatewayEdgeOptions, CloudFrontEdgeOptions } from "../../generated/config.js";

/** Declares CloudFront as the edge the project's hostnames are served from. */
export function cloudfront(options: CloudFrontEdgeOptions = {}): EdgeDescriptor {
  return { cloudfront: options };
}

/** Declares API Gateway as the edge the project's hostnames are served from. */
export function apiGateway(options: ApiGatewayEdgeOptions = {}): EdgeDescriptor {
  return { "api-gateway": options };
}
