import { describe, expect, it } from "vitest";
import { EXACT_JSON } from "../testing/exact-json.js";
import { JsonText } from "./json-text.js";
import { encodePayload } from "./payload.js";

describe("JsonText", () => {
  it("keeps the text it is given byte for byte", () => {
    expect(new JsonText(` ${EXACT_JSON}\n`).text).toBe(` ${EXACT_JSON}\n`);
  });

  it("refuses text that is not JSON with a SyntaxError", () => {
    expect(() => new JsonText("{not json")).toThrow(SyntaxError);
  });

  it("encodes as the value it holds when nested in another value", () => {
    expect(JSON.stringify({ seen: new JsonText('{ "a": [1, 2] }') })).toBe('{"seen":{"a":[1,2]}}');
  });

  it("is sent as a payload byte for byte", () => {
    expect(new TextDecoder().decode(encodePayload(new JsonText(EXACT_JSON)))).toBe(EXACT_JSON);
  });

  it("is refused as a payload over the limit, counted in the bytes of its text", () => {
    expect(() => encodePayload(new JsonText(`"${"a".repeat(262_144)}"`))).toThrow(/at most/);
  });
});
