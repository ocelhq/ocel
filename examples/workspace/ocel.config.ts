import type { AppConfig } from "ocel/config";
import { defineConfig } from "ocel/config";

const run = process.env.OCEL_JOURNEY_RUN;
const slug = run ? `j-${run}-workspace` : "workspace";

const config = defineConfig({
  slug,
  discovery: { paths: ["../next/ocel", "../express/ocel", "../hono/ocel"] },
  apps: [
    { name: "next", framework: "next", path: "../next", folder: "/next" },
    { name: "express", framework: "express", path: "../express", folder: "/express" },
    { name: "hono", framework: "hono", path: "../hono", folder: "/hono" },
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
