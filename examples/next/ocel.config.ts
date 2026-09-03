import type { AppConfig } from "ocel/config";
import { defineConfig } from "ocel/config";
import { APP, productionHostname, projectSlug } from "./lib/hostname";

const config = defineConfig({
  slug: projectSlug(),
  apps: [{ name: APP, framework: "next", path: "." }],
});

export function zonedApps(): AppConfig[] | undefined {
  return config.apps?.map((app) => {
    const production = productionHostname(app.name);
    return production ? { ...app, domains: { production } } : app;
  });
}

export default config;
