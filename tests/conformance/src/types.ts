export const conformanceCapabilities = [
  "http",
  "static",
  "postgres",
  "blob",
  "env",
  "native",
  "isr",
  "revalidate",
  "proxy",
  "golden",
  "bytecode",
  "links",
] as const;

export type Capability = (typeof conformanceCapabilities)[number];

export type TargetName = "dev" | "aws";

export type LinkTool = "sst" | "pulumi";

export type Example = {
  name:
    | "express"
    | "hono"
    | "next"
    | "fastify"
    | "with-transforms"
    | "with-sst"
    | "with-pulumi";
  framework: "next" | "express" | "hono" | "fastify";
  appName: string;
  dir: string;
  startCmd: string[];
  capabilities: readonly Capability[];
  targets?: readonly TargetName[];
  linkTool?: LinkTool;
};

export type LinkReport = {
  host: string;
  port: string;
  database: string;
  hasPassword: boolean;
  connected: boolean;
};

export type TargetHandle = {
  baseUrl: string;
  teardown: () => Promise<void>;
  headObject: (key: string) => Promise<{ contentType?: string }>;
  assertBytecodeArchive: () => Promise<void>;
  assertBytecodeEmbeddedArtifact: () => Promise<void>;
  assertBytecodeColdStart: () => Promise<void>;
  linkReport?: LinkReport;
  output?: () => string;
};

export type Target = {
  name: TargetName;
  up: (example: Example) => Promise<TargetHandle>;
};

export type CheckContext = {
  example: Example;
  baseUrl: () => string;
  headObject: (key: string) => Promise<{ contentType?: string }>;
  runId: string;
  targetName: TargetName;
  output: () => string;
  linkReport: () => LinkReport;
  assertBytecodeArchive: () => Promise<void>;
  assertBytecodeEmbeddedArtifact: () => Promise<void>;
  assertBytecodeColdStart: () => Promise<void>;
};

export type Check = (context: CheckContext) => void;
