import vps from "@ocel/provider-vps";
import { buildEnv, defineConfig } from "ocel/config";
import { z } from "zod";

const ssh = buildEnv({
  OCEL_VPS_HOST: z.string().min(1),
  OCEL_VPS_USER: z.string().min(1),
  OCEL_VPS_IDENTITY_FILE: z.string().min(1),
});

export default defineConfig({
  slug: "ocelhq",
  provider: vps({
    ssh: {
      host: ssh.OCEL_VPS_HOST,
      user: ssh.OCEL_VPS_USER,
      identityFile: ssh.OCEL_VPS_IDENTITY_FILE,
    },
  }),
  apps: [{ name: "node", path: "./tests/fixtures/sdk/node" }],
});
