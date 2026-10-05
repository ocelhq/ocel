export interface ImageOriginRequest {
  assetPrefix: string;
  url: string;
  w: number;
  q: number;
  accept: string;
  mimeType: string;
  configHash: string;
}

export interface CompiledRemotePattern {
  protocol?: string;
  hostname: string;
  port?: string;
  pathname: string;
  search?: string;
}

export interface CompiledLocalPattern {
  pathname: string;
  search?: string;
}

export interface CompiledImageConfig {
  path: string;
  deviceSizes: number[];
  imageSizes: number[];
  qualities?: number[];
  formats: string[];
  domains: string[];
  minimumCacheTTL: number;
  maximumRedirects: number;
  maximumResponseBody: number;
  dangerouslyAllowSVG: boolean;
  dangerouslyAllowLocalIP: boolean;
  contentSecurityPolicy: string;
  contentDispositionType: string;
  remotePatterns: CompiledRemotePattern[];
  localPatterns?: CompiledLocalPattern[];
}

export interface OriginResponse {
  status: number;
  headers: Record<string, string>;
  body: Uint8Array;
}

export const IMAGE_PASSTHROUGH = "x-ocel-image-passthrough";
