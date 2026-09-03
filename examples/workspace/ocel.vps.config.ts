import { buildEnv, defineConfig } from "ocel/config";
import vps from "@ocel/provider-vps";
import { z } from "zod";
import base from "./ocel.config.ts";

const env = buildEnv({
  OCEL_VPS_HOST: z.string(),
  OCEL_VPS_USER: z.string(),
  OCEL_VPS_IDENTITY_FILE: z.string(),
});

export default defineConfig({
  ...base,
  provider: vps({
    ssh: {
      host: env.OCEL_VPS_HOST,
      user: env.OCEL_VPS_USER,
      identityFile: env.OCEL_VPS_IDENTITY_FILE,
    },
  }),
});
