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
  : {
      // What every ordinary build and deploy gets. Next allows no remote host by
      // default, so the deployed app's own remote image needs one entry — without
      // it Next, and the edge that reimplements Next, both answer /_next/image
      // with 400 "url" parameter is not allowed.
      images: {
        remotePatterns: [{ protocol: "https", hostname: "picsum.photos" }],
      },
    };

export default nextConfig;
