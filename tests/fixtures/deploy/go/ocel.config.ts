import { defineConfig } from "ocel/config";
import awsProvider from "ocel/providers/aws";

export default defineConfig({
  slug: "go",
  provider: awsProvider(),
  apps: [
    {
      name: "web",
      path: "./server",
      runtime: "go",
      // The architecture the binary is built for, x86_64 unless named:
      // runtime: { name: "go", arch: "arm64" },
    },
  ],
});
