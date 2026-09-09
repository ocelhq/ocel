import { defineConfig } from "ocel/config";
import awsProvider from "ocel/providers/aws";

export default defineConfig({
  slug: "sdk-rust",
  provider: awsProvider(),
  apps: [
    {
      name: "web",
      path: ".",
    },
  ],
});
