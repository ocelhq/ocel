import { describe, expect, it } from "vitest";
import { sanitizeName } from "./layout.js";

describe("sanitizeName", () => {
  it("keeps a clean name", () => expect(sanitizeName("acme-web")).toBe("acme-web"));
  it("replaces unsafe runs and trims dashes", () => expect(sanitizeName("@acme/web app")).toBe("acme-web-app"));
  it("can reduce to empty", () => expect(sanitizeName("@/")).toBe(""));
});
