import awsProvider from "@ocel/provider-aws";
import { defineConfig } from "ocel/config";
import { cloudflare } from "ocel/edge";
import base from "./ocel.config";

export default defineConfig({
  ...base,
  provider: awsProvider({ transforms: ["./infra/network.transform.ts"] }),
  edge: cloudflare(),
});
