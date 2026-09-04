import type { AppConfig } from "ocel/config";
import { defineConfig } from "ocel/config";

const slug = process.env.OCEL_JOURNEY_SLUG ?? "express";

const config = defineConfig({
  slug,
  apps: [{ name: "web", framework: "express", path: "." }],
});

export function zonedApps(compute?: string): AppConfig[] | undefined {
  const zone = process.env.OCEL_JOURNEY_ZONE;
  return config.apps?.map((app) => {
    const zoned = zone ? { ...app, domains: { production: `${app.name}-${slug}.${zone}` } } : app;
    return compute === "container" ? { ...zoned, compute } : zoned;
  });
}

export default config;
