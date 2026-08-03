import { createHash } from "node:crypto";
import { createRequire } from "node:module";
import type {
  ImageConfigComplete,
  LocalPattern,
  RemotePattern,
} from "next/dist/shared/lib/image-config.js";
import { stableStringify } from "./stable-json.mjs";

// The app's own Next, not this package's: the compiled patterns are matched at
// request time by a worker that carries no glob library, so the only way the
// edge and `next dev` can agree is for the build to compile them with the very
// picomatch the app's Next would have matched with.
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

// The image config as the edge and the optimizer read it: every glob already a
// regex source, and nothing in it that does not bear on serving a request.
// `localPatterns` absent means "every local path is allowed" and `qualities`
// absent means "any quality in 1..100" — both are Next's own readings of the
// unset value, so absence has to survive serialization as absence.
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

// dot:true on pathnames and not on hostnames is Next's own asymmetry, kept by
// construction: a dotfile path is servable, a leading-dot host label is not.
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

// A URL is a legal remotePatterns entry and does not survive JSON, so it is
// read down to the same fields matchRemotePattern would have read off it —
// including its always-present pathname and search, which is what makes a URL
// entry stricter than the object form.
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

// Undefined when the app has opted out of the built-in optimizer, which is a
// valid, common setup rather than a build error.
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

// The bytes written to image-config.json, and the bytes configHash is taken
// over: the optimizer hashes the object it loaded from S3 and refuses it unless
// the edge's configHash matches, so the two sides have to canonicalize
// identically or every request fails closed.
export function serializeImageConfig(config: CompiledImageConfig): string {
  return stableStringify(config);
}

export function imageConfigHash(config: CompiledImageConfig): string {
  return createHash("sha256")
    .update(serializeImageConfig(config))
    .digest("hex");
}
