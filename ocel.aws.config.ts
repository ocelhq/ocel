import awsProvider from "@ocel/provider-aws";
import { buildEnv, defineConfig } from "ocel/config";
import { cloudflareDns } from "ocel/dns";
import { cloudflare } from "ocel/edge";
import { z } from "zod";

const aws = buildEnv({
  OCEL_AWS_REGION: z.string().min(1),
  OCEL_AWS_VARS_KEY: z.string().startsWith("arn:aws:kms:"),
});

export default defineConfig({
  slug: "ocelhq",
  edge: cloudflare(),
  dns: cloudflareDns(),
  provider: awsProvider({ region: aws.OCEL_AWS_REGION, varsKey: aws.OCEL_AWS_VARS_KEY }),
  apps: [
    { name: "www", runtime: "next", path: "./www" },
    {
      name: "github",
      runtime: "node",
      path: "./console/github",
      domains: { production: "github.ocel.dev" },
    },
  ],
});
