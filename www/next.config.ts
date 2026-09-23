import { createMDX } from "fumadocs-mdx/next";
import type { NextConfig } from "next";

const nextConfig: NextConfig = {
  reactCompiler: true,
  async headers() {
    return [
      {
        source: "/schema/:version/ocel.schema.json",
        headers: [
          { key: "content-type", value: "application/schema+json; charset=utf-8" },
          { key: "cache-control", value: "public, max-age=3600" },
        ],
      },
    ];
  },
};

const withMDX = createMDX();

export default withMDX(nextConfig);
