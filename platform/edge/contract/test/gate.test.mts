import { describe, expect, it } from "vitest";

import rawVectors from "../../../../pkg/edge/edgeconformance/gate.json" with { type: "json" };
import gateEncodings from "../fixtures/gate.json" with { type: "json" };
import { checkGate, decodeMountedGate, type Gate, hashPassword, parseGate } from "../src/gate.mjs";

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
  gate?: Partial<Gate>;
  request: {
    method: string;
    host: string;
    target: string;
    headers: Record<string, string | string[]>;
  };
  expect: Expectation;
}

const vectors = rawVectors as unknown as { nowSeconds: number; gate: Gate; cases: Case[] };

function encodeWireHeaders(headers: Case["request"]["headers"]): Headers {
  const wire = new Headers();
  for (const [name, values] of Object.entries(headers)) {
    for (const value of [values].flat()) {
      wire.append(name, String.fromCharCode(...new TextEncoder().encode(value)));
    }
  }
  return wire;
}

describe("the gate answers every conformance vector", () => {
  for (const example of vectors.cases) {
    it(example.name, async () => {
      const gate: Gate = { ...vectors.gate, ...example.gate };
      const verdict = await checkGate(
        gate,
        {
          method: example.request.method,
          host: example.request.host,
          target: example.request.target,
          headers: encodeWireHeaders(example.request.headers),
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
  expect(await hashPassword("test-preview-key", "JBSWY3DPEHPK3PXP2345")).toBe(
    "f18e0cd484c475051052b9fed4f125e1db487c694fc74b1128b40bb4af41e5a3",
  );
});

describe("a gate's keys read back from the JSON Go encodes them as", () => {
  for (const example of gateEncodings.gates) {
    it(example.name, () => {
      expect(parseGate(JSON.parse(example.encoded))).toEqual(example.gate);
    });
  }

  it("the shared entry's record of a slug", () => {
    const { slug, encoded } = gateEncodings.sharedEntry;
    const recorded = JSON.parse(encoded) as Record<string, unknown>;
    expect(parseGate(recorded[slug])).toEqual(gateEncodings.gates[0]?.gate);
  });
});

describe("a mounted gate", () => {
  for (const example of gateEncodings.gates) {
    it(`decodes ${example.name} from the JSON Go mounts`, () => {
      expect(decodeMountedGate(example.encoded)).toEqual(example.gate);
    });
  }

  it("decodes a withdrawn gate to no gate", () => {
    expect(decodeMountedGate("null\n")).toBeUndefined();
  });

  it("refuses contents it cannot read rather than opening the host", () => {
    for (const contents of ["", "{", "[]", "true", '{"key":"","passwordMac":"m"}']) {
      expect(() => decodeMountedGate(contents), contents).toThrow();
    }
  });
});

describe("parsing a gate's keys", () => {
  it("reads absent or null lists as empty", () => {
    for (const lists of [{}, { bypassSecrets: null, allowOptions: null }]) {
      expect(parseGate({ key: "k", passwordMac: "m", ...lists })).toEqual({
        key: "k",
        passwordMac: "m",
        bypassSecrets: [],
        allowOptions: [],
      });
    }
  });

  it("refuses keys it cannot gate with", () => {
    for (const value of [
      undefined,
      null,
      [],
      "gate",
      {},
      { key: "", passwordMac: "m" },
      { key: "k" },
      { key: "k", passwordMac: "m", bypassSecrets: "one" },
      { key: "k", passwordMac: "m", allowOptions: [1] },
    ]) {
      expect(parseGate(value), JSON.stringify(value)).toBeUndefined();
    }
  });
});
