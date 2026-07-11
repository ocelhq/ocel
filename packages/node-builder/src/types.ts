export interface AppInput {
  /** Function name; drives the `<name>.func` artifact directory. */
  name: string;
  /** Optional stable identifier the Go orchestrator maps to IaC. */
  logicalName?: string;
  /** App root directory (where the framework project lives). */
  cwd: string;
  /** Explicit entrypoint relative to `cwd`; skips candidate resolution. */
  entrypoint?: string;
  /** Framework key; defaults to "express". */
  framework?: string;
}

export interface BuildOptions {
  /** Output root, corresponds to `.ocel/output` in a real project. */
  outDir: string;
}

/**
 * One entry of the stdout `functions[]` contract consumed by the Go
 * orchestrator. See README for the full contract shape.
 */
export interface FunctionSummary {
  name: string;
  logicalName?: string;
  runtime: string;
  handler: string;
  /** `.func` directory relative to `outDir` (e.g. `functions/api.func`). */
  artifactPath: string;
  framework: string;
}
