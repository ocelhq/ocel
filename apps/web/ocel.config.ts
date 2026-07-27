import awsProvider from "@ocel/provider-aws";
import { defineConfig } from "@ocel/sdk/config";


export default defineConfig({
  slug: "ocel-web",
  provider: awsProvider()
});
