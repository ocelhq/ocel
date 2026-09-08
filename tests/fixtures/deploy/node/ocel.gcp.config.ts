import gcpProvider from "@ocel/provider-gcp";
import { buildEnv, defineConfig } from "ocel/config";
import { z } from "zod";

const gcp = buildEnv({
  OCEL_GCP_PROJECT: z.string().min(1),
  OCEL_GCP_REGION: z.string().min(1),
});

export default defineConfig({
  slug: "node",
  provider: gcpProvider({ project: gcp.OCEL_GCP_PROJECT, region: gcp.OCEL_GCP_REGION }),
  apps: [
    {
      name: "web",
      path: ".",
      // Serverless unless told otherwise; a container is one image serving every route:
      // compute: "container",
    },
  ],
});
