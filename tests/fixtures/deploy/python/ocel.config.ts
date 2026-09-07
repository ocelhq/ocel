import awsProvider from "@ocel/provider-aws";
import { defineConfig } from "ocel/config";

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
