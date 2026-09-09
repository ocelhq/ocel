import { defineConfig } from "ocel/config";
import awsProvider from "ocel/providers/aws";

export default defineConfig({
  slug: "python",
  provider: awsProvider(),
  apps: [
    {
      name: "web",
      path: "./server",
      runtime: "python",
      // The architecture the wheels are vendored for, x86_64 unless named:
      // runtime: { name: "python", arch: "arm64" },
    },
  ],
});
