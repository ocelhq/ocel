import { defineConfig } from "ocel/config";
import awsProvider from "@ocel/provider-aws";

export default defineConfig({
  slug: "adapter2",
  provider: awsProvider(),
  domains: {
    preview: "*.ocel.site"
  }
});
