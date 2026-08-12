import type { TestProject } from "vitest/node";
import { applyE2EEnvDefaults } from "./env";

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
