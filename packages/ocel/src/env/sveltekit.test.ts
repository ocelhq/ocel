import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { z } from "zod";

const declareEnvMock = vi.hoisted(() => vi.fn(() => Promise.resolve({ cells: [] as unknown[] })));
const reportEnvProblemsMock = vi.hoisted(() => vi.fn(() => Promise.resolve({})));

vi.mock("../runtime/rpc", () => ({
  rpc: {
    resource: {
      declare: vi.fn(),
      declareEnv: declareEnvMock,
      reportEnvProblems: reportEnvProblemsMock,
    },
  },
}));

const { defineEnvVars, EnvDefinitionError } = await import("./sveltekit.js");

interface Declared {
  key: string;
  class: number;
  required: boolean;
  folders: string[];
  description?: string;
}

async function declared(): Promise<Declared[]> {
  await Promise.all(globalThis.__ocelRegister ?? []);
  globalThis.__ocelRegister = [];
  const [call] = declareEnvMock.mock.calls as unknown as [[{ definitions: Declared[] }]];
  return call[0].definitions;
}

beforeEach(() => {
  declareEnvMock.mockClear();
  reportEnvProblemsMock.mockClear();
});

afterEach(() => {
  vi.unstubAllEnvs();
});

describe("the variables ocel/env/sveltekit hands to SvelteKit", () => {
  it("keeps every field SvelteKit owns and drops the ones ocel owns", () => {
    const schema = z.string().url();
    const variables = defineEnvVars({
      STRIPE_KEY: { class: "sensitive", description: "Stripe key", folders: ["/web"] },
      PUBLIC_API_URL: { class: "plain", public: true, static: true, schema },
    });

    expect(variables.STRIPE_KEY).toEqual({ description: "Stripe key" });
    expect(variables.PUBLIC_API_URL).toEqual({ public: true, static: true, schema });
  });

  it("turns a validator function into the standard schema SvelteKit reads", () => {
    const variables = defineEnvVars({
      PORT_HINT: {
        class: "plain",
        schema: (value: string | undefined) => (value === undefined ? 3000 : Number(value)),
      },
    });

    const result = variables.PORT_HINT.schema["~standard"].validate("8080");
    expect(result).toEqual({ value: 8080 });
  });

  it("adds the deployment url as a public variable SvelteKit reads from the environment", () => {
    const variables = defineEnvVars({});

    expect(variables.PUBLIC_OCEL_URL).toMatchObject({ public: true });
    const { schema } = variables.PUBLIC_OCEL_URL;
    expect(schema["~standard"].validate("https://web.example.com")).toEqual({
      value: "https://web.example.com",
    });
    expect(schema["~standard"].validate(undefined)).toEqual({ value: undefined });
  });

  it("refuses a declared PUBLIC_OCEL_URL, because ocel writes it", () => {
    expect(() => defineEnvVars({ PUBLIC_OCEL_URL: { class: "plain", public: true } })).toThrow(
      /deployment\.url/,
    );
  });
});

describe("the declaration ocel/env/sveltekit makes through core defineEnv", () => {
  it("declares each variable with its class, scope, description and whether it is required", async () => {
    vi.stubEnv("OCEL_PHASE", "discovery");
    defineEnvVars({
      STRIPE_KEY: { class: "sensitive", description: "Stripe key", folders: ["/web"] },
      PUBLIC_API_URL: { class: "plain", public: true },
      PORT_HINT: { class: "plain", schema: () => 3000 },
    });

    expect(await declared()).toEqual([
      {
        key: "STRIPE_KEY",
        class: 2,
        required: true,
        folders: ["/web"],
        source: expect.any(String),
        description: "Stripe key",
      },
      {
        key: "PUBLIC_API_URL",
        class: 1,
        required: true,
        folders: [],
        source: expect.any(String),
      },
      { key: "PORT_HINT", class: 1, required: false, folders: [], source: expect.any(String) },
    ]);
  });

  it("does not declare the deployment url, which ocel writes", async () => {
    vi.stubEnv("OCEL_PHASE", "discovery");
    defineEnvVars({ STRIPE_KEY: { class: "sensitive" } });

    expect((await declared()).map((definition) => definition.key)).toEqual(["STRIPE_KEY"]);
  });
});

describe("what ocel/env/sveltekit refuses that SvelteKit does not", () => {
  it("refuses a public variable of a confidential class", () => {
    for (const variableClass of ["sensitive", "secret"] as const) {
      expect(() =>
        // @ts-expect-error the pairing this asserts on does not typecheck
        defineEnvVars({ PUBLIC_LEAK: { class: variableClass, public: true } }),
      ).toThrow(EnvDefinitionError);
    }
  });

  it("refuses class secret and points at the accessor of core defineEnv that rotates", () => {
    expect(() =>
      // @ts-expect-error a secret is read once at startup under SvelteKit
      defineEnvVars({ SIGNING_KEY: { class: "secret" } }),
    ).toThrow(/SIGNING_KEY.*secret.*defineEnv.*ocel\/env/s);
  });

  it("refuses a static sensitive variable, which would inline its value into the server bundle", () => {
    expect(() =>
      // @ts-expect-error a static sensitive value is inlined into the bundle
      defineEnvVars({ STRIPE_KEY: { class: "sensitive", static: true } }),
    ).toThrow(/STRIPE_KEY.*static.*inline/s);
  });

  it("allows a static plain variable and a dynamic sensitive one", () => {
    expect(() =>
      defineEnvVars({
        BUILD_TAG: { class: "plain", static: true },
        STRIPE_KEY: { class: "sensitive" },
      }),
    ).not.toThrow();
  });

  it("withholds the schema message of a sensitive value, since it can quote the value", () => {
    const echoing = z.string().refine(() => false, { error: (issue) => `received ${issue.input}` });
    const variables = defineEnvVars({ STRIPE_KEY: { class: "sensitive", schema: echoing } });

    const result = variables.STRIPE_KEY.schema["~standard"].validate("sk_live_123");
    const message = JSON.stringify(result);
    expect(message).toContain("withheld");
    expect(message).not.toContain("sk_live_123");
  });

  it("keeps the schema message of a plain value, which is safe to show", () => {
    const echoing = z.string().refine(() => false, { error: (issue) => `received ${issue.input}` });
    const variables = defineEnvVars({ API_URL: { class: "plain", schema: echoing } });

    const result = variables.API_URL.schema["~standard"].validate("nope");
    expect(JSON.stringify(result)).toContain("received nope");
  });
});
