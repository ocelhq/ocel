import { applyConsoleEnvDefaults } from "@ocel-tests/shared/env";
import type { TestProject } from "vitest/node";

export default async function setup(project: TestProject) {
  applyConsoleEnvDefaults();

  let token = process.env.OCEL_ACCESS_TOKEN;
  if (!token) {
    const { seed } = await import("@ocel-tests/shared/seed");
    token = (await seed("E2E")).token;
  }

  project.provide("accessToken", token);
}

declare module "vitest" {
  interface ProvidedContext {
    accessToken: string;
  }
}
