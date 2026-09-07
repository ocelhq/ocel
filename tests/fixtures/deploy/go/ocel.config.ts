import awsProvider from "@ocel/provider-aws";
import { defineConfig } from "ocel/config";

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
