import { afterEach, describe, expect, it, vi } from "vitest";
import { z } from "zod";

const declareEnvMock = vi.hoisted(() => vi.fn(() => Promise.resolve({ cells: [] as unknown[] })));

vi.mock("../runtime/rpc", () => ({
  rpc: {
    resource: {
      declare: vi.fn(),
      declareEnv: declareEnvMock,
      reportEnvProblems: vi.fn(() => Promise.resolve({})),
    },
  },
}));

const { defineEnv: defineServer, EnvDefinitionError, group } = await import("./next.js");
const { defineEnv: defineEdge } = await import("./next-edge.js");

afterEach(() => {
  vi.unstubAllEnvs();
  globalThis.__ocelRegister = [];
});

describe.each([
  ["server", defineServer],
  ["edge", defineEdge],
] as const)("the %s entry of ocel/env/next", (_name, defineEnv) => {
  it("reads a public variable from the inlined values even when the runtime environment differs", () => {
    vi.stubEnv("OCEL_PHASE", "");
    vi.stubEnv("NEXT_PUBLIC_API_URL", "https://runtime.example.com");
    vi.stubEnv(
      "OCEL_PUBLIC_ENV",
      JSON.stringify({ NEXT_PUBLIC_API_URL: "https://built.example.com" }),
    );
    const env = defineEnv({ NEXT_PUBLIC_API_URL: { class: "plain" } });

    expect(env.NEXT_PUBLIC_API_URL).toBe("https://built.example.com");
  });

  it("reads a public variable from the runtime environment when no values were inlined", () => {
    vi.stubEnv("OCEL_PHASE", "");
    vi.stubEnv("NEXT_PUBLIC_API_URL", "https://runtime.example.com");
    const env = defineEnv({ NEXT_PUBLIC_API_URL: { class: "plain" } });

    expect(env.NEXT_PUBLIC_API_URL).toBe("https://runtime.example.com");
  });

  it("parses an inlined public variable through its schema", () => {
    vi.stubEnv("OCEL_PHASE", "");
    vi.stubEnv("OCEL_PUBLIC_ENV", JSON.stringify({ NEXT_PUBLIC_PORT: "8080" }));
    const env = defineEnv({ NEXT_PUBLIC_PORT: { class: "plain", schema: z.coerce.number() } });

    expect(env.NEXT_PUBLIC_PORT).toBe(8080);
  });

  it("reads a variable without the prefix from the runtime environment, never from the inlined values", () => {
    vi.stubEnv("OCEL_PHASE", "");
    vi.stubEnv("INTERNAL_URL", "http://internal");
    vi.stubEnv("OCEL_PUBLIC_ENV", JSON.stringify({ INTERNAL_URL: "http://inlined" }));
    const env = defineEnv({ INTERNAL_URL: { class: "plain" } });

    expect(env.INTERNAL_URL).toBe("http://internal");
  });

  it("reads a group of public variables from the inlined values", () => {
    vi.stubEnv("OCEL_PHASE", "");
    vi.stubEnv(
      "OCEL_PUBLIC_ENV",
      JSON.stringify({ NEXT_PUBLIC_POSTHOG_KEY: "phc_1", NEXT_PUBLIC_POSTHOG_HOST: "https://ph" }),
    );
    const env = defineEnv({
      posthog: group({
        NEXT_PUBLIC_POSTHOG_KEY: { class: "plain" },
        NEXT_PUBLIC_POSTHOG_HOST: { class: "plain" },
      }),
    });

    expect(env.posthog).toEqual({
      NEXT_PUBLIC_POSTHOG_KEY: "phc_1",
      NEXT_PUBLIC_POSTHOG_HOST: "https://ph",
    });
  });

  it("reads a group that mixes public and private variables from the runtime environment", () => {
    vi.stubEnv("OCEL_PHASE", "");
    vi.stubEnv("NEXT_PUBLIC_POSTHOG_KEY", "phc_runtime");
    vi.stubEnv("POSTHOG_HOST", "https://ph");
    vi.stubEnv("OCEL_PUBLIC_ENV", JSON.stringify({ NEXT_PUBLIC_POSTHOG_KEY: "phc_built" }));
    const env = defineEnv({
      posthog: group({
        NEXT_PUBLIC_POSTHOG_KEY: { class: "plain" },
        POSTHOG_HOST: { class: "plain" },
      }),
    });

    expect(env.posthog).toEqual({
      NEXT_PUBLIC_POSTHOG_KEY: "phc_runtime",
      POSTHOG_HOST: "https://ph",
    });
  });

  it("refuses a NEXT_PUBLIC_ variable of a confidential class when it is declared", () => {
    for (const variableClass of ["sensitive", "secret"] as const) {
      expect(() =>
        // @ts-expect-error the pairing this asserts on does not typecheck
        defineEnv({ NEXT_PUBLIC_TOKEN: { class: variableClass } }),
      ).toThrow(EnvDefinitionError);
    }
  });

  it("refuses a NEXT_PUBLIC_ variable of a confidential class inside a group", () => {
    expect(() =>
      defineEnv({
        // @ts-expect-error the pairing this asserts on does not typecheck
        keys: group({
          NEXT_PUBLIC_TOKEN: { class: "sensitive" },
        }),
      }),
    ).toThrow(/NEXT_PUBLIC_TOKEN/);
  });
});

describe("the server entry of ocel/env/next", () => {
  it("still declares every variable, public or not, during discovery", async () => {
    vi.stubEnv("OCEL_PHASE", "discovery");
    declareEnvMock.mockClear();
    defineServer({ NEXT_PUBLIC_API_URL: { class: "plain" }, STRIPE_KEY: { class: "secret" } });
    await Promise.all(globalThis.__ocelRegister ?? []);

    const [call] = declareEnvMock.mock.calls as unknown as [
      [{ definitions: Array<{ key: string }> }],
    ];
    expect(call[0].definitions.map((definition) => definition.key)).toEqual([
      "NEXT_PUBLIC_API_URL",
      "STRIPE_KEY",
    ]);
  });
});
