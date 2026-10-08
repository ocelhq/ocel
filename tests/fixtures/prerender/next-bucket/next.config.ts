import type { NextConfig } from "next";

const nextConfig: NextConfig = {
  deploymentId: process.env.NEXT_DEPLOYMENT_ID,
};

export default nextConfig;
