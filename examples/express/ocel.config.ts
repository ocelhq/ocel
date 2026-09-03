import type { AppConfig } from "ocel/config";
import { defineConfig } from "ocel/config";

const run = process.env.OCEL_JOURNEY_RUN;
const slug = run ? `j-${run}-express` : "express";

const config = defineConfig({
  slug,
  apps: [{ name: "web", framework: "express", path: "." }],
});

export function zonedApps(): AppConfig[] | undefined {
  const zone = process.env.OCEL_JOURNEY_ZONE;
  if (!zone) {
    return config.apps;
  }
  return config.apps?.map((app) => ({
    ...app,
    domains: { production: `${app.name}.${slug}.${zone}` },
  }));
}

export default config;
