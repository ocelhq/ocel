import vps from "@ocel/provider-vps";
import { defineConfig } from "ocel/config";

function required(name: string): string {
  const value = process.env[name];
  if (!value) {
    throw new Error(`${name} names the box this deploys to, and nothing here spells one out`);
  }
  return value;
}

export default defineConfig({
  slug: "ocelhq",
  provider: vps({
    ssh: {
      host: required("OCEL_VPS_HOST"),
      user: required("OCEL_VPS_USER"),
      identityFile: required("OCEL_VPS_IDENTITY_FILE"),
    },
  }),
  apps: [{ name: "express", framework: "express", path: "./examples/express" }],
});
