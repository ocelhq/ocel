import type { AppConfig } from "ocel/config";
import { defineConfig } from "ocel/config";
import { APP, productionHostname, projectSlug } from "./lib/hostname";

const config = defineConfig({
  slug: projectSlug(),
  apps: [{ name: APP, framework: "next", path: "." }],
});

export function zonedApps(compute?: string): AppConfig[] | undefined {
  return config.apps?.map((app) => {
    const production = productionHostname(app.name);
    const zoned = production ? { ...app, domains: { production } } : app;
    return compute === "container" ? { ...zoned, compute } : zoned;
  });
}

export default config;
