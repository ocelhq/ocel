export interface AppInput {
  /** Function name; drives the `<name>.func` artifact directory. */
  name: string;
  /** App root directory (where the framework project lives). */
  cwd: string;
  /** Explicit entrypoint relative to `cwd`; skips candidate resolution. */
  entrypoint?: string;
  /** Framework key; when absent it is auto-detected from `cwd`. */
  framework?: string;
}

export interface BuildOptions {
  /** Output root, corresponds to `.ocel/output` in a real project. */
  outDir: string;
}

/**
 * The result of building one app. The builder is otherwise "dumb": it writes
 * each `.func` (with its `config.json`) under `outDir` and the Go CLI discovers
 * functions by walking that tree — this summary is an in-process return value,
 * not a cross-process contract.
 */
export interface FunctionSummary {
  name: string;
  runtime: string;
  handler: string;
  /** `.func` directory relative to `outDir` (e.g. `functions/api.func`). */
  artifactPath: string;
  framework: string;
}
