import type { NextConfig } from "next";

const nextConfig: NextConfig = {
  adapterPath: require.resolve("@ocel/next-runtime"),
};

export default nextConfig;
