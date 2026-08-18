import { defineConfig } from "ocel/config";
import { cfEdge } from "ocel/edge";
import awsProvider from "@ocel/provider-aws";

export default defineConfig({
  slug: "with-pulumi",
  provider: awsProvider({ transforms: ["./infra/network.transform.ts"] }),
  edge: cfEdge(),
  links: ["orders"],
  apps: [{ name: "api", framework: "express", path: "." }],
});
