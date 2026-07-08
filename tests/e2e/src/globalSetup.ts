import type { TestProject } from "vitest/node";
import { applyE2EEnvDefaults } from "./env";

// Resolves the bearer token every spec authenticates the CLI with. In CI the
// workflow seeds once and exports OCEL_ACCESS_TOKEN, so we reuse it. Locally
// (no token provided) we mint one here so `vitest run` works with zero setup.
export default async function setup(project: TestProject) {
  applyE2EEnvDefaults();

  let token = process.env.OCEL_ACCESS_TOKEN;
  if (!token) {
    const { seed } = await import("./seed");
    token = (await seed()).token;
  }

  project.provide("accessToken", token);
}

declare module "vitest" {
  interface ProvidedContext {
    accessToken: string;
  }
}
