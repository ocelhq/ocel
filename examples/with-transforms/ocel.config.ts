import { defineConfig } from "ocel/config";
import { cfEdge } from "ocel/edge";
import awsProvider from "@ocel/provider-aws";

export default defineConfig({
  slug: "with-transforms",
  provider: awsProvider({ transforms: ["./infra/defaults.transform.ts"] }),
  edge: cfEdge(),
  apps: [{ name: "api", framework: "express", path: "." }],
});
