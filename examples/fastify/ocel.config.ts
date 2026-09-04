import type { AppConfig } from "ocel/config";
import { defineConfig } from "ocel/config";

const run = process.env.OCEL_JOURNEY_RUN;
const slug = run ? `j-${run}-fastify` : "fastify";

const config = defineConfig({
  slug,
  apps: [{ name: "web", framework: "fastify", path: "." }],
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
