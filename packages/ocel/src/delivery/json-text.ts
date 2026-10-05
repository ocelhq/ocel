const jsonTextBrand = Symbol.for("ocel.JsonText");

/**
 * JSON text kept byte for byte as it was written, where `JSON.parse` turns `2.0` into `2` and
 * rounds an integer past 2^53. A run reads the text its payload was sent as from `payloadJson`;
 * returned from a run, it is recorded as the output verbatim; given to `trigger` or `send`, it is
 * sent as the payload verbatim; and `runs.retrieve` answers a run's payload and output as it.
 */
export class JsonText {
  /** The JSON text. */
  readonly text: string;

  /** Wraps `text`; throws a `SyntaxError` when it is not JSON. */
  constructor(text: string) {
    JSON.parse(text);
    this.text = text;
    Object.defineProperty(this, jsonTextBrand, { value: true });
  }

  /** The value the text holds, which `JSON.stringify` writes when this is nested in another value. */
  toJSON(): unknown {
    return JSON.parse(this.text);
  }
}

export function isJsonText(value: unknown): value is JsonText {
  return typeof value === "object" && value !== null && jsonTextBrand in value;
}
