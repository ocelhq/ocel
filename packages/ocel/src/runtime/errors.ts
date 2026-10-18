/**
 * Thrown when the ocel runtime fails or refuses a call a resource made. `code` is the
 * Connect code it answered with, such as `"not_found"` or `"unavailable"`, and `cause` is the
 * transport's own error.
 */
export class RuntimeCallError extends Error {
  override name = "RuntimeCallError";

  /** The procedure that was called, as `<service>/<method>`. */
  readonly procedure: string;

  /** The Connect code the runtime answered with, in its wire form, such as `"not_found"`. */
  readonly code: string;

  constructor(procedure: string, code: string, message: string, cause: unknown) {
    super(`${procedure} failed: [${code}] ${message}`, { cause });
    this.procedure = procedure;
    this.code = code;
  }
}
