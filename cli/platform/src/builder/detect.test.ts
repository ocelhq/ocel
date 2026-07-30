import { mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";
import { afterAll, describe, expect, it } from "vitest";
import { hasDep, sanitizeName } from "./detect.js";

const roots: string[] = [];
afterAll(() => roots.forEach((d) => rmSync(d, { recursive: true, force: true })));

function dirWith(pkg: unknown): string {
  const dir = mkdtempSync(path.join(tmpdir(), "detect-"));
  roots.push(dir);
  writeFileSync(path.join(dir, "package.json"), JSON.stringify(pkg));
  return dir;
}

describe("hasDep", () => {
  it("finds a dependency", () => {
    expect(hasDep(dirWith({ dependencies: { next: "16" } }), "next")).toBe(true);
  });
  it("finds a devDependency", () => {
    expect(hasDep(dirWith({ devDependencies: { express: "5" } }), "express")).toBe(true);
  });
  it("is false for an absent dep", () => {
    expect(hasDep(dirWith({ dependencies: { express: "5" } }), "next")).toBe(false);
  });
  it("is false when package.json is missing", () => {
    const dir = mkdtempSync(path.join(tmpdir(), "detect-empty-"));
    roots.push(dir);
    expect(hasDep(dir, "express")).toBe(false);
  });
  it("is false for malformed package.json", () => {
    const dir = mkdtempSync(path.join(tmpdir(), "detect-bad-"));
    roots.push(dir);
    writeFileSync(path.join(dir, "package.json"), "{ not json");
    expect(hasDep(dir, "express")).toBe(false);
  });
});

describe("sanitizeName", () => {
  it("keeps a clean name", () => expect(sanitizeName("acme-web")).toBe("acme-web"));
  it("replaces unsafe runs and trims dashes", () => expect(sanitizeName("@acme/web app")).toBe("acme-web-app"));
  it("can reduce to empty", () => expect(sanitizeName("@/")).toBe(""));
});
