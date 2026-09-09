import { buildEnv, defineConfig } from "ocel/config";
import gcpProvider from "ocel/providers/gcp";
import { z } from "zod";

const gcp = buildEnv({
  OCEL_GCP_PROJECT: z.string().min(1),
  OCEL_GCP_REGION: z.string().min(1),
});

export default defineConfig({
  slug: "python",
  provider: gcpProvider({ project: gcp.OCEL_GCP_PROJECT, region: gcp.OCEL_GCP_REGION }),
  apps: [
    {
      name: "web",
      path: "./server",
      runtime: "python",
    },
  ],
});
