import { describe, expect, it } from "vitest";

import rawVectors from "../../../../pkg/edge/edgeconformance/previewgate.json" with {
  type: "json",
};
import { checkPreview, hashPreviewPassword, type PreviewGate } from "../src/preview-gate.mjs";

interface Expectation {
  forward?: boolean;
  forwardHeaders?: Record<string, string>;
  responseHeaders?: Record<string, string>;
  status?: number;
  headers?: Record<string, string>;
  bodyContains?: string[];
  bodyExcludes?: string[];
  absent?: string[];
}

interface Case {
  name: string;
  gate?: Partial<PreviewGate>;
  request: {
    method: string;
    host: string;
    target: string;
    headers: Record<string, string>;
    body?: string;
  };
  expect: Expectation;
}

const vectors = rawVectors as unknown as { now: number; gate: PreviewGate; cases: Case[] };

describe("the preview gate answers every conformance vector", () => {
  for (const c of vectors.cases) {
    it(c.name, async () => {
      const gate: PreviewGate = { ...vectors.gate, ...c.gate };
      const verdict = await checkPreview(
        gate,
        {
          method: c.request.method,
          host: c.request.host,
          target: c.request.target,
          headers: new Headers(c.request.headers),
          body: c.request.body,
        },
        vectors.now,
      );

      if (c.expect.forward) {
        expect(verdict.kind, "forwarded").toBe("forward");
        if (verdict.kind !== "forward") return;
        for (const [name, want] of Object.entries(c.expect.forwardHeaders ?? {})) {
          expect(verdict.headers.get(name), name).toBe(want);
        }
        for (const name of c.expect.absent ?? [])
          expect(verdict.headers.has(name), name).toBe(false);
        for (const [name, want] of Object.entries(c.expect.responseHeaders ?? {})) {
          expect(verdict.responseHeaders.get(name), name).toBe(want);
        }
        return;
      }

      expect(verdict.kind, "answered").toBe("respond");
      if (verdict.kind !== "respond") return;
      expect(verdict.response.status).toBe(c.expect.status);
      for (const [name, want] of Object.entries(c.expect.headers ?? {})) {
        expect(verdict.response.headers.get(name), name).toBe(want);
      }
      for (const name of c.expect.absent ?? [])
        expect(verdict.response.headers.has(name), name).toBe(false);
      for (const want of c.expect.bodyContains ?? []) expect(verdict.response.body).toContain(want);
      for (const unwanted of c.expect.bodyExcludes ?? [])
        expect(verdict.response.body).not.toContain(unwanted);
    });
  }
});

it("hashes a password to the value the vectors store", async () => {
  expect(await hashPreviewPassword("test-preview-key", "JBSWY3DPEHPK3PXP2345")).toBe(
    "f18e0cd484c475051052b9fed4f125e1db487c694fc74b1128b40bb4af41e5a3",
  );
});
