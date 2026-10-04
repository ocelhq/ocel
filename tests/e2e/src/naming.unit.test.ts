import { describe, expect, it } from "bun:test";
import { sanitize } from "./naming";

describe("sanitize", () => {
  it("keeps the alphabet pkg/naming's Sanitize keeps, case for case", () => {
    const cases: [string, string][] = [
      ["Web/API/Users", "web-api-users"],
      ["web_api_users", "web-api-users"],
      ["--leading--and--trailing--", "leading-and-trailing"],
      ["", "x"],
      ["...", "x"],
    ];
    for (const [value, sanitized] of cases) {
      expect(sanitize(value)).toBe(sanitized);
    }
  });
});
