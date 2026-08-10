import type { NextConfig } from "next";

const nextConfig: NextConfig = {
  reactCompiler: true,
  transpilePackages: ["@repo/api", "@repo/auth", "@repo/db", "@repo/resources"],
};

export default nextConfig;
