export const sdkCapabilities = [
  "http",
  "static",
  "postgres",
  "blob",
  "env",
  "native",
  "isr",
  "revalidate",
] as const;

export type Capability = (typeof sdkCapabilities)[number];

export type Example = {
  name: "express" | "hono" | "next" | "fastify" | "with-transforms";
  framework: "next" | "express" | "hono" | "fastify";
  appName: string;
  dir: string;
  startCmd: string[];
  capabilities: readonly Capability[];
};

export type TargetHandle = {
  baseUrl: string;
  teardown: () => Promise<void>;
  output?: () => string;
};

export type Target = {
  name: "dev" | "aws";
  up: (example: Example) => Promise<TargetHandle>;
};

export type CheckContext = {
  example: Example;
  baseUrl: () => string;
  runId: string;
};

export type Check = (context: CheckContext) => void;
