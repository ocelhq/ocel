import awsProvider from "@ocel/provider-aws";
import { defineConfig } from "ocel/config";

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
