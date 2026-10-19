import { defineConfig } from "ocel/config";
import awsProvider from "ocel/providers/aws";

export default defineConfig({
  slug: "nestjs",
  // Deploys into the region the environment names. Pin one instead:
  // provider: awsProvider({ region: "eu-west-1" }),
  provider: awsProvider(),
  apps: [
    {
      name: "web",
      path: ".",
    },
  ],
});
