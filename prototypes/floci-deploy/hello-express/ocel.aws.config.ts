import { defineConfig } from "ocel/config";
import awsProvider from "@ocel/provider-aws";
import base from "./ocel.config";

export default defineConfig({
  ...base,
  provider: awsProvider({ region: "us-east-1" }),
  domains: { production: "hello-express.example.test" },
});
