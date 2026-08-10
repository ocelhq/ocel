import type { NextConfig } from "next";

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
      images: {
        remotePatterns: [
          { protocol: "https", hostname: "picsum.photos" },
          { protocol: "https", hostname: "fastly.picsum.photos" },
        ],
      },
    };

export default nextConfig;
