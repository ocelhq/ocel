import type { NextConfig } from "next";

// The image conformance fixtures (workers/nextjs/scripts/generate-image-fixtures.mjs)
// need an allowlist with something in it, and both settings of dangerouslyAllowSVG
// — which is baked into the build, so it takes one build per value. Unset, which
// is every ordinary build and deploy, this leaves Next's own defaults in place.
const fixtures = process.env.OCEL_IMAGE_FIXTURES;

const nextConfig: NextConfig = fixtures
  ? {
      images: {
        remotePatterns: [
          { protocol: "https", hostname: "**.allowed.example", pathname: "/img/**" },
        ],
        domains: ["legacy.example"],
        dangerouslyAllowSVG: fixtures === "svg",
      },
    }
  : {};

export default nextConfig;
