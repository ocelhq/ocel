export interface AppInput {
  name: string;
  cwd: string;
  entrypoint?: string;
  framework?: string;
  env?: Record<string, string>;
  folder?: string;
}

export interface FrameworkSpec {
  name: string;
  runtime: string;
  entrypointCandidates: string[];
}

export interface BuildOptions {
  outDir: string;
  edgeKind?: string;
  allowDegraded?: string[];
}

export interface FunctionSummary {
  name: string;
  runtime: string;
  handler: string;
  artifactPath: string;
  framework: string;
  strategy: "trace" | "bundle";
  entrypoint?: string;
}
