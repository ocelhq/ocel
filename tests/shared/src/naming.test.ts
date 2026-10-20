import { expect, test } from "bun:test";
import fixture from "../fixtures/namespace-fields.json";
import { namespaceField } from "./naming";

test("a namespace's field is the one Go fits it to", () => {
  for (const { namespace, field } of fixture) {
    expect(namespaceField(namespace)).toBe(field);
  }
});
