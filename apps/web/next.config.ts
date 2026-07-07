import type { NextConfig } from "next";

const nextConfig: NextConfig = {
  /* config options here */
  reactCompiler: true,
  // @repo/db and @repo/resources ship raw TS with no build step.
  transpilePackages: ["@repo/db", "@repo/resources"],
};

export default nextConfig;
