import { mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";
import { afterAll, describe, expect, it } from "vitest";
import { detectFramework, resolveFramework } from "./registry.js";

const roots: string[] = [];
afterAll(() => roots.forEach((d) => rmSync(d, { recursive: true, force: true })));

function dirWith(deps: Record<string, string>): string {
  const dir = mkdtempSync(path.join(tmpdir(), "registry-"));
  roots.push(dir);
  writeFileSync(path.join(dir, "package.json"), JSON.stringify({ dependencies: deps }));
  return dir;
}

describe("resolveFramework", () => {
  it("resolves a known key", () => expect(resolveFramework("express").name).toBe("express"));
  it("throws naming known frameworks for an unknown key", () => {
    expect(() => resolveFramework("svelte")).toThrow(/unknown framework "svelte".*next/s);
  });
});

describe("detectFramework", () => {
  it("detects each framework by its dep", () => {
    expect(detectFramework(dirWith({ express: "5" }))?.name).toBe("express");
    expect(detectFramework(dirWith({ fastify: "5" }))?.name).toBe("fastify");
    expect(detectFramework(dirWith({ next: "16" }))?.name).toBe("next");
  });
  it("prefers next over express when both are present", () => {
    expect(detectFramework(dirWith({ next: "16", express: "5" }))?.name).toBe("next");
  });
  it("returns undefined when nothing matches", () => {
    expect(detectFramework(dirWith({ lodash: "4" }))).toBeUndefined();
  });
});
