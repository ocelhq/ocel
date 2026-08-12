import { defineConfig } from "ocel/config";
import awsProvider from "@ocel/provider-aws";

export default defineConfig({
  slug: "e2e-31323687264-87121599",
  provider: awsProvider(),
});
