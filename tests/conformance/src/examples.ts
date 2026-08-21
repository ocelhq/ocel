import path from "node:path";
import { fileURLToPath } from "node:url";
import type { Example } from "./types";

const here = path.dirname(fileURLToPath(import.meta.url));
export const repoRoot = path.resolve(here, "..", "..", "..");
const examplesDir = path.join(repoRoot, "examples");

export const examples = [
  {
    name: "express",
    framework: "express",
    appName: "exp",
    dir: path.join(examplesDir, "express"),
    startCmd: ["pnpm", "start"],
    capabilities: ["http", "postgres", "blob", "native"],
  },
  {
    name: "hono",
    framework: "hono",
    appName: "hono",
    dir: path.join(examplesDir, "hono"),
    startCmd: ["pnpm", "start"],
    capabilities: ["http", "postgres", "blob"],
  },
  {
    name: "next",
    framework: "next",
    appName: "next-app",
    dir: path.join(examplesDir, "next"),
    startCmd: ["pnpm", "start"],
    capabilities: [
      "http",
      "static",
      "postgres",
      "blob",
      "env",
      "isr",
      "revalidate",
    ],
  },
  {
    name: "fastify",
    framework: "fastify",
    appName: "fstfy",
    dir: path.join(examplesDir, "fastify"),
    startCmd: ["pnpm", "start"],
    capabilities: ["http", "postgres", "blob"],
  },
  {
    name: "with-transforms",
    framework: "express",
    appName: "api",
    dir: path.join(examplesDir, "with-transforms"),
    startCmd: ["pnpm", "start"],
    capabilities: ["http", "postgres"],
  },
  {
    name: "with-sst",
    framework: "express",
    appName: "api",
    dir: path.join(examplesDir, "with-sst"),
    startCmd: ["pnpm", "start"],
    capabilities: ["links"],
    targets: ["aws"],
    linkTool: "sst",
  },
  {
    name: "with-pulumi",
    framework: "express",
    appName: "api",
    dir: path.join(examplesDir, "with-pulumi"),
    startCmd: ["pnpm", "start"],
    capabilities: ["links"],
    targets: ["aws"],
    linkTool: "pulumi",
  },
] as const satisfies readonly Example[];
