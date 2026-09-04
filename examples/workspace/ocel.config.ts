import type { AppConfig } from "ocel/config";
import { defineConfig } from "ocel/config";

const slug = process.env.OCEL_JOURNEY_SLUG ?? "workspace";

const config = defineConfig({
  slug,
  apps: [
    { name: "next", framework: "next", path: "./apps/next", folder: "/next" },
    { name: "express", framework: "express", path: "./apps/express", folder: "/express" },
  ],
});

export function zonedApps(): AppConfig[] | undefined {
  const zone = process.env.OCEL_JOURNEY_ZONE;
  if (!zone) {
    return config.apps;
  }
  return config.apps?.map((app) => ({
    ...app,
    domains: { production: `${app.name}-${slug}.${zone}` },
  }));
}

export default config;
