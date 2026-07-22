import { describe, expect, it } from "vitest";

import { normalizeBaseDomain, previewPointer } from "../src/preview";

describe("previewPointer", () => {
  it("extracts the subdomain label directly under the base domain", () => {
    expect(previewPointer("flaky-web-2626.myapp.com", "myapp.com")).toBe(
      "flaky-web-2626",
    );
  });

  it("lowercases the host and ignores the port", () => {
    expect(previewPointer("Flaky-Web-2626.MyApp.com:8787", "myapp.com")).toBe(
      "flaky-web-2626",
    );
  });

  it("returns null for the apex (no subdomain label)", () => {
    expect(previewPointer("myapp.com", "myapp.com")).toBeNull();
  });

  it("returns null for a host not under the base domain", () => {
    expect(previewPointer("flaky-web-2626.other.com", "myapp.com")).toBeNull();
    expect(previewPointer("myapp.com.evil.com", "myapp.com")).toBeNull();
  });

  it("returns null for a multi-label subdomain the wildcard would not match", () => {
    expect(previewPointer("a.b.myapp.com", "myapp.com")).toBeNull();
  });

  it("returns null when the label is empty", () => {
    expect(previewPointer(".myapp.com", "myapp.com")).toBeNull();
  });

  it("tolerates a base domain given with surrounding dots", () => {
    expect(previewPointer("flaky-web-2626.myapp.com", ".myapp.com.")).toBe(
      "flaky-web-2626",
    );
  });

  it("returns null when the base domain is empty", () => {
    expect(previewPointer("flaky-web-2626.myapp.com", "")).toBeNull();
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
