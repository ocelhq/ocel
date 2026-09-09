import { buildEnv, defineConfig } from "ocel/config";
import gcpProvider from "ocel/providers/gcp";
import { z } from "zod";

const gcp = buildEnv({
  OCEL_GCP_PROJECT: z.string().min(1),
  OCEL_GCP_REGION: z.string().min(1),
});

export default defineConfig({
  slug: "workspace",
  provider: gcpProvider({ project: gcp.OCEL_GCP_PROJECT, region: gcp.OCEL_GCP_REGION }),
  apps: [
    {
      name: "next",
      runtime: "next",
      path: "./apps/next",
      folder: "/next",
    },
    {
      name: "express",
      path: "./apps/express",
      folder: "/express",
    },
  ],
});
