import { defineConfig } from "ocel/config";
import awsProvider from "ocel/providers/aws";

export default defineConfig({
  slug: "sdk-go",
  provider: awsProvider(),
  apps: [
    {
      name: "web",
      path: "./server",
      runtime: "go",
    },
  ],
});
