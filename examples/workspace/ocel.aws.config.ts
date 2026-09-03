import { defineConfig } from "ocel/config";
import { cloudflare } from "ocel/edge";
import aws from "@ocel/provider-aws";
import base from "./ocel.config.ts";

export default defineConfig({
  ...base,
  provider: aws(),
  edge: cloudflare(),
});
