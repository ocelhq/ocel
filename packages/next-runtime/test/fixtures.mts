// The `images` config Next 16.2.10 hands the adapter for an app that configures
// nothing — captured from a real build (examples/next-test/args.json).
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
