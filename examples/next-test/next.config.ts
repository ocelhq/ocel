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
      // default, so the deployed app's own remote image needs an entry — without
      // one, Next and the edge that reimplements it both answer /_next/image
      // with 400 "url" parameter is not allowed.
      //
      // Both hosts, because picsum.photos serves nothing itself: it 302s to
      // fastly.picsum.photos. Next follows that hop without consulting the
      // allowlist again, and the optimizer here deliberately does not
      // (divergence 1, packages/image-optimizer/src/upstream.mts) — an open
      // redirect on one tenant's allowlisted CDN would otherwise be a fetch
      // primitive aimed at every other tenant in the account. So an app
      // deployed on Ocel names the host its images actually come from.
      images: {
        remotePatterns: [
          { protocol: "https", hostname: "picsum.photos" },
          { protocol: "https", hostname: "fastly.picsum.photos" },
        ],
      },
    };

export default nextConfig;
