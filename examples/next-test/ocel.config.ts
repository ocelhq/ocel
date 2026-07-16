import { defineConfig } from "@ocel/sdk/config";
import awsProvider from "@ocel/provider-aws";

export default defineConfig({
  projectId: "nextest",
  provider: awsProvider(),
  domains: {
    production: "nextest.ocel.dev"
  }
});
