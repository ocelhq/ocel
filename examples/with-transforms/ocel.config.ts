import { defineConfig } from "ocel/config";
import awsProvider from "@ocel/provider-aws";

export default defineConfig({
  slug: "with-transforms",
  provider: awsProvider({ transforms: ["./infra/defaults.transform.ts"] }),
  apps: [{ name: "api", framework: "express", path: "." }],
});
