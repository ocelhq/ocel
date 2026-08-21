import type { TestProject } from "vitest/node";
import { applyDevEnvDefaults } from "./env";

export default async function setup(project: TestProject) {
  applyDevEnvDefaults();

  if (process.env.OCEL_CONFORMANCE_TARGET === "registry") {
    project.provide("accessToken", "");
    return;
  }

  let token = process.env.OCEL_ACCESS_TOKEN;
  if (!token) {
    if (process.env.OCEL_CONFORMANCE_TARGET === "aws") {
      throw new Error("OCEL_ACCESS_TOKEN is required by the aws target");
    }
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
