import awsProvider from "@ocel/provider-aws";
import { defineConfig } from "ocel/config";


export default defineConfig({
  slug: "ocel-web",
  provider: awsProvider()
});
