import type { NextConfig } from "next";

const fixtures = process.env.OCEL_IMAGE_FIXTURES;

const nextConfig: NextConfig = {
  cacheComponents: true,
  cacheLife: {
    editorial: {
      stale: 600,
      revalidate: 3600,
      expire: 86400,
    },
  },
  images: fixtures
    ? {
        remotePatterns: [
          {
            protocol: "https",
            hostname: "**.allowed.example",
            pathname: "/img/**",
          },
        ],
        domains: ["legacy.example"],
        dangerouslyAllowSVG: fixtures === "svg",
      }
    : {
        remotePatterns: [
          { protocol: "https", hostname: "picsum.photos" },
          { protocol: "https", hostname: "fastly.picsum.photos" },
        ],
      },
};

export default nextConfig;
