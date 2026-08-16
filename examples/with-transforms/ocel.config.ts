import { defineConfig } from "ocel/config";
import awsProvider from "@ocel/provider-aws";

export default defineConfig({
  slug: "with-transforms",
  provider: awsProvider({ transforms: ["./ocel/transform.ts"] }),
  apps: [{ name: "api", framework: "express", path: "." }],
});
