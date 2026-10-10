import { describe, expect, it } from "vitest";

import rawVectors from "../../../../pkg/edge/edgeconformance/previewgate.json" with {
  type: "json",
};
import { checkPreview, hashPreviewPassword, type PreviewGate } from "../src/preview-gate.mjs";

interface Expectation {
  forward?: boolean;
  forwardHeaders?: Record<string, string>;
  forwardHeadersAbsent?: string[];
  responseHeaders?: Record<string, string>;
  status?: number;
  headers?: Record<string, string>;
  headersAbsent?: string[];
  bodyContains?: string[];
  bodyExcludes?: string[];
}

interface Case {
  name: string;
  gate?: Partial<PreviewGate>;
  request: {
    method: string;
    host: string;
    target: string;
    headers: Record<string, string | string[]>;
    body?: string;
  };
  expect: Expectation;
}

const vectors = rawVectors as unknown as { nowSeconds: number; gate: PreviewGate; cases: Case[] };

function encodeWireHeaders(headers: Case["request"]["headers"]): Headers {
  const wire = new Headers();
  for (const [name, values] of Object.entries(headers)) {
    for (const value of [values].flat()) {
      wire.append(name, String.fromCharCode(...new TextEncoder().encode(value)));
    }
  }
  return wire;
}

describe("the preview gate answers every conformance vector", () => {
  for (const example of vectors.cases) {
    it(example.name, async () => {
      const gate: PreviewGate = { ...vectors.gate, ...example.gate };
      const verdict = await checkPreview(
        gate,
        {
          method: example.request.method,
          host: example.request.host,
          target: example.request.target,
          headers: encodeWireHeaders(example.request.headers),
          body: example.request.body,
        },
        vectors.nowSeconds,
      );

      if (example.expect.forward) {
        expect(verdict.kind, "forwarded").toBe("forward");
        if (verdict.kind !== "forward") return;
        for (const [name, want] of Object.entries(example.expect.forwardHeaders ?? {})) {
          expect(verdict.headers.get(name), name).toBe(want);
        }
        for (const name of example.expect.forwardHeadersAbsent ?? [])
          expect(verdict.headers.has(name), name).toBe(false);
        for (const [name, want] of Object.entries(example.expect.responseHeaders ?? {})) {
          expect(verdict.responseHeaders.get(name), name).toBe(want);
        }
        return;
      }

      expect(verdict.kind, "answered").toBe("respond");
      if (verdict.kind !== "respond") return;
      expect(verdict.response.status).toBe(example.expect.status);
      for (const [name, want] of Object.entries(example.expect.headers ?? {})) {
        expect(verdict.response.headers.get(name), name).toBe(want);
      }
      for (const name of example.expect.headersAbsent ?? [])
        expect(verdict.response.headers.has(name), name).toBe(false);
      for (const want of example.expect.bodyContains ?? [])
        expect(verdict.response.body).toContain(want);
      for (const unwanted of example.expect.bodyExcludes ?? [])
        expect(verdict.response.body).not.toContain(unwanted);
    });
  }
});

it("hashes a password to the value the vectors store", async () => {
  expect(await hashPreviewPassword("test-preview-key", "JBSWY3DPEHPK3PXP2345")).toBe(
    "f18e0cd484c475051052b9fed4f125e1db487c694fc74b1128b40bb4af41e5a3",
  );
});
