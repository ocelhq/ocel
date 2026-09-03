import { defineConfig } from "ocel/config";
import awsProvider from "@ocel/provider-aws";
import { apiGateway } from "@ocel/provider-aws/edge";
import base from "./ocel.config";

export default defineConfig({
  ...base,
  provider: awsProvider({ region: "us-east-1" }),
  edge: apiGateway(),
  domains: { production: "hello-express.example.test" },
});
