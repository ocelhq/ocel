import awsProvider from "@ocel/provider-aws";
import { defineConfig } from "@ocel/sdk/config";


export default defineConfig({
  slug: "ocel-web",
  projectId: "019f40d2-d02f-7726-b8d7-fe37034ff4f0",
  provider: awsProvider()
});
