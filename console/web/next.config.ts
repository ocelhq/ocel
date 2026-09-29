import type { NextConfig } from "next";

const nextConfig: NextConfig = {
  reactCompiler: true,
  transpilePackages: [
    "@console/api",
    "@console/auth",
    "@console/connectors",
    "@console/db",
    "@console/resources",
    "@ui/variables",
  ],
};

export default nextConfig;
