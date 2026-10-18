/**
 * Thrown when a resource's binding was never delivered to this process. Match it with
 * `instanceof` to tell a resource this deploy does not include from one that is misconfigured.
 */
export class MissingBindingError extends Error {
  override name = "MissingBindingError";

  /** The key the binding is delivered under, as a live value, a file in `OCEL_LIVE_DIR` or a variable. */
  readonly key: string;

  constructor(key: string) {
    super(
      `${key} is not delivered to this process: \`ocel dev\` delivers it locally and \`ocel deploy\` to the deployed app, and a build gets it only from an \`ocel deploy\` whose provider forwards a port to the resource, so code that runs while building, such as prerendering a page, cannot use it under \`ocel build\``,
    );
    this.key = key;
  }
}
