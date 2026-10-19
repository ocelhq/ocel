import { scryptSync } from "node:crypto";

import { describe, expect, it } from "vitest";

import rawVectors from "../../../../pkg/edge/edgeconformance/gate.json" with { type: "json" };
import gateEncodings from "../fixtures/gate.json" with { type: "json" };
import {
  checkGate,
  decodeMountedGate,
  type Gate,
  hashPassword,
  hasPassword,
  parseGate,
} from "../src/gate.mjs";

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

function scryptHash(password: string, logN: number, r: number, p: number): string {
  const salt = Buffer.from("sixteen byte slt");
  const derived = scryptSync(password, salt, 32, { N: 2 ** logN, r, p, maxmem: 256 * 1024 * 1024 });
  const encode = (bytes: Buffer) => bytes.toString("base64").replace(/=+$/, "");
  return ["scrypt", logN, r, p, encode(salt), encode(derived)].join("$");
}

function gateHolding(passwordHash: string): Gate {
  return { key: "k", passwordHash, bypassSecrets: [], allowOptions: [] };
}

describe("a gate's password", { timeout: 30_000 }, () => {
  it("is hashed with scrypt at the OWASP cost and a fresh salt", async () => {
    const [first, second] = [await hashPassword("pw"), await hashPassword("pw")];

    const fields = first.split("$");
    expect(fields.slice(0, 4)).toEqual(["scrypt", "14", "8", "5"]);
    expect(Buffer.from(fields[4] ?? "", "base64")).toHaveLength(16);
    expect(Buffer.from(fields[5] ?? "", "base64")).toHaveLength(32);
    expect(second).not.toBe(first);
  });

  it("is the one its hash was made from and no other", async () => {
    const gate = gateHolding(await hashPassword("correct horse battery"));

    expect(await hasPassword(gate, "correct horse battery")).toBe(true);
    for (const other of ["correct horse batterz", "", "correct horse battery "]) {
      expect(await hasPassword(gate, other), other).toBe(false);
    }
  });

  it("matches an scrypt hash made elsewhere from the same parameters", async () => {
    expect(await hasPassword(gateHolding(scryptHash("pw", 14, 8, 5)), "pw")).toBe(true);
  });

  it("is refused against an scrypt hash costlier than the bounds even when it matches", async () => {
    for (const [logN, r, p] of [
      [15, 8, 1],
      [14, 9, 1],
      [13, 8, 6],
    ] as const) {
      const hash = scryptHash("pw", logN, r, p);
      expect(await hasPassword(gateHolding(hash), "pw"), hash).toBe(false);
    }
  });

  it("is derived once however often it is checked", async () => {
    const gate = gateHolding(await hashPassword("checked often"));
    const started = performance.now();
    await hasPassword(gate, "checked often");
    await hasPassword(gate, "not it");
    const derivations = performance.now() - started;

    const repeatedFrom = performance.now();
    for (let i = 0; i < 20; i++) {
      expect(await hasPassword(gate, "checked often")).toBe(true);
      expect(await hasPassword(gate, "not it")).toBe(false);
    }
    expect(performance.now() - repeatedFrom).toBeLessThan(derivations);
  });

  it("is refused against a hash the gate cannot parse", async () => {
    const valid = scryptHash("pw", 14, 8, 5);
    const fields = valid.split("$");
    const salt = fields[4] ?? "";
    const derived = fields[5] ?? "";
    const withField = (index: number, value: string) =>
      fields.map((field, at) => (at === index ? value : field)).join("$");
    for (const [name, hash] of Object.entries({
      empty: "",
      "no parameters": "scrypt",
      "an unknown algorithm": withField(0, "bcrypt"),
      "an upper-case algorithm": withField(0, "SCRYPT"),
      "a padded cost": withField(1, "014"),
      "a signed cost": withField(1, "+14"),
      "a zero cost": withField(1, "0"),
      "a zero block size": withField(2, "0"),
      "a zero parallelism": withField(3, "0"),
      "a padded salt": withField(4, `${salt}==`),
      "a short salt": withField(4, salt.slice(0, 20)),
      "a non-canonical salt": withField(4, `${salt.slice(0, 21)}B`),
      "a short derived key": withField(5, derived.slice(0, 40)),
      "a padded derived key": withField(5, `${derived}=`),
      "an extra field": `${valid}$x`,
      "a signature, which only an edge that cannot afford scrypt is handed":
        "hmac-sha256$f18e0cd484c475051052b9fed4f125e1db487c694fc74b1128b40bb4af41e5a3",
    })) {
      const gate = { ...gateHolding(hash), key: "test-preview-key" };
      const password = name.startsWith("a signature") ? "JBSWY3DPEHPK3PXP2345" : "pw";
      expect(await hasPassword(gate, password), name).toBe(false);
    }
  });
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
    for (const contents of ["", "{", "[]", "true", '{"key":"","passwordHash":"m"}']) {
      expect(() => decodeMountedGate(contents), contents).toThrow();
    }
  });
});

describe("parsing a gate's keys", () => {
  it("reads absent or null lists as empty", () => {
    for (const lists of [{}, { bypassSecrets: null, allowOptions: null }]) {
      expect(parseGate({ key: "k", passwordHash: "m", ...lists })).toEqual({
        key: "k",
        passwordHash: "m",
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
      { key: "", passwordHash: "m" },
      { key: "k" },
      { key: "k", passwordHash: "m", bypassSecrets: "one" },
      { key: "k", passwordHash: "m", allowOptions: [1] },
    ]) {
      expect(parseGate(value), JSON.stringify(value)).toBeUndefined();
    }
  });
});
