import vpsProvider from "@ocel/provider-vps";
import { buildEnv, defineConfig } from "ocel/config";
import { z } from "zod";

// What the config itself needs while it is evaluated, read from the shell or the project's .env.
const ssh = buildEnv({
  OCEL_VPS_HOST: z.string().min(1),
  OCEL_VPS_USER: z.string().min(1),
  OCEL_VPS_IDENTITY_FILE: z.string().min(1),
});

export default defineConfig({
  slug: "node",
  provider: vpsProvider({
    ssh: {
      host: ssh.OCEL_VPS_HOST,
      user: ssh.OCEL_VPS_USER,
      identityFile: ssh.OCEL_VPS_IDENTITY_FILE,
    },
  }),
  apps: [
    {
      name: "web",
      path: ".",
      compute: "container",
      health: { path: "/health" },
      // The hostname production serves on, bound with `ocel domain add`:
      // domains: { production: "web.example.com" },
    },
  ],
});
