import { describe, expect, it, vi } from "vitest";
import { assertBytecodeConformance } from "../src/checks/bytecode";

describe("bytecode conformance", () => {
  it("checks the cache archive, deployed artifact, and cold-start read", async () => {
    const calls: string[] = [];
    await assertBytecodeConformance({
      output: () =>
        "warmed 1/1 bundles\nembedded 1/1 compile caches",
      assertBytecodeArchive: vi.fn(async () => {
        calls.push("archive");
      }),
      assertBytecodeEmbeddedArtifact: vi.fn(async () => {
        calls.push("artifact");
      }),
      assertBytecodeColdStart: vi.fn(async () => {
        calls.push("cold-start");
      }),
    });
    expect(calls).toEqual(["archive", "artifact", "cold-start"]);
  });

  it("rejects an incomplete deploy before inspecting its output", async () => {
    const archive = vi.fn(async () => {});
    await expect(
      assertBytecodeConformance({
        output: () =>
          "warmed 0/1 bundles\nembedded 0/1 compile caches",
        assertBytecodeArchive: archive,
        assertBytecodeEmbeddedArtifact: vi.fn(async () => {}),
        assertBytecodeColdStart: vi.fn(async () => {}),
      }),
    ).rejects.toThrow();
    expect(archive).not.toHaveBeenCalled();
  });
});
