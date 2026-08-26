import { defineConfig } from "ocel/config";
import vps from "@ocel/provider-vps";

export default defineConfig({
  slug: "ocelhq",
  provider: vps({
    ssh: {
      host: "10.160.227.140",
      user: "ubuntu",
      identityFile: "/home/vndaba/.local/state/ocel-incus/id_ed25519",
    },
  }),
  apps: [{ name: "express", framework: "express", path: "./examples/express" }],
});
