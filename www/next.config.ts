import { createMDX } from "fumadocs-mdx/next";
import type { NextConfig } from "next";
import { posthogRewrites } from "./lib/posthog";

const alternate = (url: string) => `<${url}>; rel="alternate"; type="text/markdown"`;

const nextConfig: NextConfig = {
  reactCompiler: true,
  async headers() {
    return [
      {
        source: "/schema/:path*",
        headers: [
          { key: "content-type", value: "application/schema+json; charset=utf-8" },
          { key: "cache-control", value: "public, max-age=3600" },
        ],
      },
      {
        source: "/docs",
        headers: [{ key: "link", value: alternate("/docs/index.md") }],
      },
      {
        source: "/docs/:path((?!.*\\.md$).+)",
        headers: [{ key: "link", value: alternate("/docs/:path.md") }],
      },
    ];
  },
  async rewrites() {
    return [
      ...posthogRewrites(process.env.NEXT_PUBLIC_POSTHOG_HOST),
      { source: "/docs/:slug*.md", destination: "/llms.mdx/docs/:slug*/content.md" },
    ];
  },
  skipTrailingSlashRedirect: true,
};

const withMDX = createMDX();

export default withMDX(nextConfig);
