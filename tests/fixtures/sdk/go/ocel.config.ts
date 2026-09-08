import awsProvider from "@ocel/provider-aws";
import { defineConfig } from "ocel/config";

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
