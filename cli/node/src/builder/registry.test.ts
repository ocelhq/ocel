import { mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";
import { afterAll, describe, expect, it } from "vitest";
import { detectRuntime, resolveRuntime } from "./registry.js";

const roots: string[] = [];
afterAll(() => roots.forEach((d) => rmSync(d, { recursive: true, force: true })));

function dirWith(deps: Record<string, string>): string {
  const dir = mkdtempSync(path.join(tmpdir(), "registry-"));
  roots.push(dir);
  writeFileSync(path.join(dir, "package.json"), JSON.stringify({ dependencies: deps }));
  return dir;
}

function emptyDir(): string {
  const dir = mkdtempSync(path.join(tmpdir(), "registry-"));
  roots.push(dir);
  return dir;
}

describe("resolveRuntime", () => {
  it("resolves a known key", () => expect(resolveRuntime("node").name).toBe("node"));
  it("resolves next", () => expect(resolveRuntime("next").name).toBe("next"));
  it("throws naming known runtimes for an unknown key", () => {
    expect(() => resolveRuntime("svelte")).toThrow(/unknown runtime "svelte".*next.*node/s);
  });
});

describe("detectRuntime", () => {
  it("reads next off the app's dependencies", () => {
    expect(detectRuntime(dirWith({ next: "16" }))?.name).toBe("next");
  });
  it("falls back to node for any other package.json", () => {
    expect(detectRuntime(dirWith({ express: "5" }))?.name).toBe("node");
    expect(detectRuntime(dirWith({ lodash: "4" }))?.name).toBe("node");
    expect(detectRuntime(dirWith({}))?.name).toBe("node");
  });
  it("prefers next over a plain node app when next is a dependency", () => {
    expect(detectRuntime(dirWith({ next: "16", express: "5" }))?.name).toBe("next");
  });
  it("returns undefined for a directory with no package.json", () => {
    expect(detectRuntime(emptyDir())).toBeUndefined();
  });
});
