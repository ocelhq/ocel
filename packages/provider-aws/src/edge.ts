import type { EdgeDescriptor } from "ocel/config";

/**
 * Options for the CloudFront edge, authored inline in `ocel.config.ts`.
 *
 * Empty at release: everything CloudFront needs comes from the provider's own
 * options, so a later option lands without a signature change.
 */
export type CloudFrontEdgeOptions = Record<string, never>;

/** Declares CloudFront as the edge the project's hostnames are served from. */
export function cloudfront(options: CloudFrontEdgeOptions = {}): EdgeDescriptor {
  return { kind: "cloudfront", options } as unknown as EdgeDescriptor;
}

/**
 * Options for the API Gateway edge, authored inline in `ocel.config.ts`.
 *
 * Empty at release: everything API Gateway needs comes from the provider's own
 * options, so a later option lands without a signature change.
 */
export type ApiGatewayEdgeOptions = Record<string, never>;

/** Declares API Gateway as the edge the project's hostnames are served from. */
export function apiGateway(options: ApiGatewayEdgeOptions = {}): EdgeDescriptor {
  return { kind: "api-gateway", options } as unknown as EdgeDescriptor;
}
