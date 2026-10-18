/** A value JSON can carry. */
export type JsonValue = string | number | boolean | null | JsonValue[] | JsonObject;

/** A JSON object. */
export type JsonObject = { [key: string]: JsonValue };
