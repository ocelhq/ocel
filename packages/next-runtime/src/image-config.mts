import { createHash } from "node:crypto";
import { createRequire } from "node:module";
import type {
  ImageConfigComplete,
  LocalPattern,
  RemotePattern,
} from "next/dist/shared/lib/image-config.js";
import { stableStringify } from "./stable-json.mjs";

const { makeRe } = createRequire(import.meta.url)(
  "next/dist/compiled/picomatch",
) as { makeRe: (glob: string, options?: { dot?: boolean }) => RegExp };

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
  formats: ImageConfigComplete["formats"];
  domains: string[];
  minimumCacheTTL: number;
  maximumRedirects: number;
  maximumResponseBody: number;
  dangerouslyAllowSVG: boolean;
  dangerouslyAllowLocalIP: boolean;
  contentSecurityPolicy: string;
  contentDispositionType: ImageConfigComplete["contentDispositionType"];
  remotePatterns: CompiledRemotePattern[];
  localPatterns?: CompiledLocalPattern[];
}

function compilePathname(glob: string | undefined): string {
  return makeRe(glob ?? "**", { dot: true }).source;
}

interface NormalizedRemotePattern {
  protocol?: string;
  hostname: string;
  port?: string;
  pathname?: string;
  search?: string;
}

function toRemotePattern(
  pattern: URL | RemotePattern,
): NormalizedRemotePattern {
  return pattern instanceof URL
    ? {
        protocol: pattern.protocol,
        hostname: pattern.hostname,
        port: pattern.port,
        pathname: pattern.pathname,
        search: pattern.search,
      }
    : pattern;
}

function optedOutOfOptimization(
  images: Required<ImageConfigComplete>,
): string | undefined {
  if (images.loader !== "default")
    return `images.loader is "${images.loader}"`;
  if (images.unoptimized) return "images.unoptimized is true";
  return undefined;
}

export function compileImageConfig(
  images: Required<ImageConfigComplete>,
): CompiledImageConfig | undefined {
  const optOut = optedOutOfOptimization(images);
  if (optOut) {
    console.warn(
      `ocel: next.config ${optOut}, so next/image emits the original src and never requests /_next/image — there is nothing to optimize and no image config is deployed.`,
    );
    return undefined;
  }

  return {
    path: images.path,
    deviceSizes: images.deviceSizes,
    imageSizes: images.imageSizes,
    ...(images.qualities && { qualities: images.qualities }),
    formats: images.formats,
    domains: images.domains,
    minimumCacheTTL: images.minimumCacheTTL,
    maximumRedirects: images.maximumRedirects,
    maximumResponseBody: images.maximumResponseBody,
    dangerouslyAllowSVG: images.dangerouslyAllowSVG,
    dangerouslyAllowLocalIP: images.dangerouslyAllowLocalIP,
    contentSecurityPolicy: images.contentSecurityPolicy,
    contentDispositionType: images.contentDispositionType,
    remotePatterns: images.remotePatterns.map(toRemotePattern).map((p) => ({
      ...(p.protocol !== undefined && {
        protocol: p.protocol.replace(/:$/, ""),
      }),
      hostname: makeRe(p.hostname).source,
      ...(p.port !== undefined && { port: p.port }),
      pathname: compilePathname(p.pathname),
      ...(p.search !== undefined && { search: p.search }),
    })),
    ...(images.localPatterns && {
      localPatterns: images.localPatterns.map((p: LocalPattern) => ({
        pathname: compilePathname(p.pathname),
        ...(p.search !== undefined && { search: p.search }),
      })),
    }),
  };
}

export function serializeImageConfig(config: CompiledImageConfig): string {
  return stableStringify(config);
}

export function imageConfigHash(config: CompiledImageConfig): string {
  return createHash("sha256")
    .update(serializeImageConfig(config))
    .digest("hex");
}
