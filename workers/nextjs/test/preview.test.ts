import { describe, expect, it } from "vitest";

import { normalizeBaseDomain, previewPointer } from "../src/preview";

describe("previewPointer", () => {
  it("strips the label suffix to recover the pointer", () => {
    expect(
      previewPointer("flaky-web-2626-a1b2c3d4e5.myapp.com", "myapp.com", "-a1b2c3d4e5"),
    ).toBe("flaky-web-2626");
  });

  it("returns null when the label does not end with the suffix", () => {
    expect(
      previewPointer("flaky-web-2626-9999999999.myapp.com", "myapp.com", "-a1b2c3d4e5"),
    ).toBeNull();
    expect(
      previewPointer("flaky-web-2626.myapp.com", "myapp.com", "-a1b2c3d4e5"),
    ).toBeNull();
  });

  it("returns null when the label is exactly the suffix, leaving no pointer", () => {
    expect(previewPointer("-a1b2c3d4e5.myapp.com", "myapp.com", "-a1b2c3d4e5")).toBeNull();
  });

  it("lowercases the host and ignores the port", () => {
    expect(
      previewPointer("Flaky-Web-2626-A1B2C3D4E5.MyApp.com:8787", "myapp.com", "-A1B2C3D4E5"),
    ).toBe("flaky-web-2626");
  });

  it("extracts the whole subdomain label when no suffix is configured", () => {
    expect(previewPointer("flaky-web-2626.myapp.com", "myapp.com", "")).toBe(
      "flaky-web-2626",
    );
    expect(previewPointer("flaky-web-2626.myapp.com", "myapp.com", undefined)).toBe(
      "flaky-web-2626",
    );
  });

  it("ignores a suffix containing a dot, which no label could ever end with", () => {
    expect(previewPointer("flaky-web-2626.myapp.com", "myapp.com", ".a1b2c3d4e5")).toBe(
      "flaky-web-2626",
    );
  });

  it("returns null for the apex (no subdomain label)", () => {
    expect(previewPointer("myapp.com", "myapp.com", "-a1b2c3d4e5")).toBeNull();
  });

  it("returns null for a host not under the base domain", () => {
    expect(
      previewPointer("flaky-web-2626-a1b2c3d4e5.other.com", "myapp.com", "-a1b2c3d4e5"),
    ).toBeNull();
    expect(previewPointer("myapp.com.evil.com", "myapp.com", "-a1b2c3d4e5")).toBeNull();
  });

  it("returns null for a multi-label subdomain no exact route would match", () => {
    expect(previewPointer("a.b-a1b2c3d4e5.myapp.com", "myapp.com", "-a1b2c3d4e5")).toBeNull();
  });

  it("returns null when the label is empty", () => {
    expect(previewPointer(".myapp.com", "myapp.com", "-a1b2c3d4e5")).toBeNull();
  });

  it("tolerates a base domain given with surrounding dots", () => {
    expect(
      previewPointer("flaky-web-2626-a1b2c3d4e5.myapp.com", ".myapp.com.", "-a1b2c3d4e5"),
    ).toBe("flaky-web-2626");
  });

  it("returns null when the base domain is empty", () => {
    expect(
      previewPointer("flaky-web-2626-a1b2c3d4e5.myapp.com", "", "-a1b2c3d4e5"),
    ).toBeNull();
  });
});

// normalizeBaseDomain is the single source of truth the fetch handler gates
// preview mode on: an empty result means "no usable base domain", which degrades
// to normal serving instead of 404-ing every request.
describe("normalizeBaseDomain", () => {
  it("lowercases and strips surrounding dots", () => {
    expect(normalizeBaseDomain(".MyApp.com.")).toBe("myapp.com");
  });

  it("treats undefined, empty, and dots-only as no base domain", () => {
    expect(normalizeBaseDomain(undefined)).toBe("");
    expect(normalizeBaseDomain("")).toBe("");
    expect(normalizeBaseDomain(".")).toBe("");
    expect(normalizeBaseDomain("...")).toBe("");
  });
});
