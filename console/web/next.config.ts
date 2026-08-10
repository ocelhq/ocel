import type { NextConfig } from "next";

const nextConfig: NextConfig = {
  reactCompiler: true,
  transpilePackages: ["@console/api", "@console/auth", "@console/db", "@console/resources"],
};

export default nextConfig;
