import vpsProvider from "@ocel/provider-vps";
import { buildEnv, defineConfig } from "ocel/config";
import { z } from "zod";
import base, { zonedApps } from "./ocel.config.ts";

const ssh = buildEnv({
  OCEL_VPS_HOST: z.string().min(1),
  OCEL_VPS_USER: z.string().min(1),
  OCEL_VPS_IDENTITY_FILE: z.string().min(1),
});

export default defineConfig({
  ...base,
  provider: vpsProvider({
    ssh: {
      host: ssh.OCEL_VPS_HOST,
      user: ssh.OCEL_VPS_USER,
      identityFile: ssh.OCEL_VPS_IDENTITY_FILE,
    },
  }),
  apps: zonedApps(),
});
