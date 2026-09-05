export interface AppInput {
  name: string;
  cwd: string;
  entrypoint?: string;
  runtime?: { name?: string; arch?: string };
  env?: Record<string, string>;
  folder?: string;
}

export interface RuntimeSpec {
  name: string;
  entrypointCandidates: string[];
}

export interface BuildOptions {
  outDir: string;
  edgeKind?: string;
  allowDegraded?: string[];
}

export interface FunctionSummary {
  name: string;
  runtime: { name: string; arch?: string };
  handler: string;
  artifactPath: string;
  strategy: "trace" | "bundle";
  entrypoint?: string;
}
