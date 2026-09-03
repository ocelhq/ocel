import type { NextConfig } from "next";

const nextConfig: NextConfig = {
  serverExternalPackages: ["better-sqlite3"],

  async headers() {
    return [
      {
        source: "/routing/:path*",
        headers: [{ key: "x-ocel-config", value: "routing" }],
      },
    ];
  },

  async redirects() {
    return [
      {
        source: "/routing/redirect/temporary",
        destination: "/routing/landing",
        permanent: false,
      },
      {
        source: "/routing/redirect/permanent",
        destination: "/routing/landing",
        permanent: true,
      },
      {
        source: "/routing/redirect/has",
        destination: "/routing/landing",
        permanent: false,
        has: [{ type: "query", key: "to", value: "landing" }],
      },
      {
        source: "/routing/redirect/missing",
        destination: "/routing/landing",
        permanent: false,
        missing: [{ type: "header", key: "x-ocel-stay" }],
      },
      {
        source: "/routing/precedence-redirect",
        destination: "/routing/landing",
        permanent: false,
      },
    ];
  },

  async rewrites() {
    return {
      beforeFiles: [
        { source: "/routing/rewrite/before", destination: "/routing/target/before" },
        { source: "/routing/precedence-redirect", destination: "/routing/target/before" },
      ],
      afterFiles: [
        { source: "/routing/rewrite/after", destination: "/routing/target/after" },
        { source: "/routing/dynamic/rewritten", destination: "/routing/target/after" },
      ],
      fallback: [{ source: "/routing/fallback/:path*", destination: "/routing/target/fallback" }],
    };
  },
};

export default nextConfig;
