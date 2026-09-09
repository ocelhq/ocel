import { defineConfig } from "ocel/config";
import awsProvider from "ocel/providers/aws";

export default defineConfig({
  slug: "sdk-python",
  provider: awsProvider(),
  apps: [
    {
      name: "web",
      path: "./server",
      runtime: "python",
    },
  ],
});
