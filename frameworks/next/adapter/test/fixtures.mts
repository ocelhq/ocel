import { mkdir, writeFile } from "node:fs/promises";
import { join } from "node:path";

export async function writePrerenderManifest(
  distDir: string,
  manifest: Record<string, unknown> = {},
): Promise<void> {
  await mkdir(distDir, { recursive: true });
  await writeFile(
    join(distDir, "prerender-manifest.json"),
    JSON.stringify({ version: 4, routes: {}, dynamicRoutes: {}, preview: {}, ...manifest }),
  );
}

export const defaultImages = {
  deviceSizes: [640, 750, 828, 1080, 1200, 1920, 2048, 3840],
  imageSizes: [32, 48, 64, 96, 128, 256, 384],
  path: "/_next/image",
  loader: "default",
  loaderFile: "",
  domains: [],
  disableStaticImages: false,
  minimumCacheTTL: 14400,
  formats: ["image/webp"],
  maximumRedirects: 3,
  maximumResponseBody: 50000000,
  dangerouslyAllowLocalIP: false,
  dangerouslyAllowSVG: false,
  contentSecurityPolicy: "script-src 'none'; frame-src 'none'; sandbox;",
  contentDispositionType: "attachment",
  localPatterns: [{ pathname: "**", search: "" }],
  remotePatterns: [],
  qualities: [75],
  unoptimized: false,
  customCacheHandler: false,
} as const;
