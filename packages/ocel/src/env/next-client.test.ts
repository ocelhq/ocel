import { afterEach, describe, expect, it, vi } from "vitest";
import { z } from "zod";
import { defineEnv, EnvClientError, EnvDefinitionError, group } from "./next-client.js";

function inlined(values: Record<string, string>) {
  vi.stubEnv("OCEL_PUBLIC_ENV", JSON.stringify(values));
}

afterEach(() => {
  vi.unstubAllEnvs();
});

describe("the browser entry of ocel/env/next", () => {
  it("reads a public variable from the values the adapter inlined", () => {
    inlined({ NEXT_PUBLIC_API_URL: "https://api.example.com" });
    const env = defineEnv({ NEXT_PUBLIC_API_URL: { class: "plain" } });

    expect(env.NEXT_PUBLIC_API_URL).toBe("https://api.example.com");
  });

  it("parses a public variable through its schema", () => {
    inlined({ NEXT_PUBLIC_PORT: "8080" });
    const env = defineEnv({ NEXT_PUBLIC_PORT: { class: "plain", schema: z.coerce.number() } });

    expect(env.NEXT_PUBLIC_PORT).toBe(8080);
  });

  it("applies a schema default to a public variable nothing was inlined for", () => {
    inlined({});
    const env = defineEnv({
      NEXT_PUBLIC_THEME: { class: "plain", schema: z.string().default("light") },
    });

    expect(env.NEXT_PUBLIC_THEME).toBe("light");
  });

  it("names the variable and the fix when a public variable without a default was never inlined", () => {
    inlined({});
    const env = defineEnv({ NEXT_PUBLIC_API_URL: { class: "plain" } });

    expect(() => env.NEXT_PUBLIC_API_URL).toThrow(/NEXT_PUBLIC_API_URL.*ocel env set/);
  });

  it("names the schema failure of an inlined value", () => {
    inlined({ NEXT_PUBLIC_API_URL: "not a url" });
    const env = defineEnv({ NEXT_PUBLIC_API_URL: { class: "plain", schema: z.string().url() } });

    expect(() => env.NEXT_PUBLIC_API_URL).toThrow(/NEXT_PUBLIC_API_URL.*schema/);
  });

  it("refuses a variable without the NEXT_PUBLIC_ prefix as server-only", () => {
    inlined({ STRIPE_KEY: "sk_live" });
    const env = defineEnv({ STRIPE_KEY: { class: "secret" } });

    expect(() => env.STRIPE_KEY).toThrow(EnvClientError);
    expect(() => env.STRIPE_KEY).toThrow(/server-only/);
  });

  it("never reveals the value of a server-only variable the define happens to hold", () => {
    inlined({ STRIPE_KEY: "sk_live" });
    const env = defineEnv({ STRIPE_KEY: { class: "plain" } });

    expect(() => env.STRIPE_KEY).toThrow(/server-only/);
  });

  it("refuses to read a variable no defineEnv call declared", () => {
    inlined({});
    const env = defineEnv({ NEXT_PUBLIC_API_URL: { class: "plain" } });

    expect(() => (env as Record<string, unknown>).NEXT_PUBLIC_OTHER).toThrow(/not a declared/);
  });

  it("refuses a NEXT_PUBLIC_ variable of a confidential class when it is declared", () => {
    for (const variableClass of ["sensitive", "secret"] as const) {
      expect(() =>
        // @ts-expect-error the pairing this asserts on does not typecheck
        defineEnv({ NEXT_PUBLIC_TOKEN: { class: variableClass } }),
      ).toThrow(EnvDefinitionError);
    }
  });

  it("explains a missing define as a build run without ocel's adapter", () => {
    const env = defineEnv({ NEXT_PUBLIC_API_URL: { class: "plain" } });

    expect(() => env.NEXT_PUBLIC_API_URL).toThrow(/adapter/);
  });

  it("reads a group of public variables as one object", () => {
    inlined({
      NEXT_PUBLIC_POSTHOG_KEY: "phc_1",
      NEXT_PUBLIC_POSTHOG_HOST: "https://ph.example.com",
    });
    const env = defineEnv({
      posthog: group({
        NEXT_PUBLIC_POSTHOG_KEY: { class: "plain" },
        NEXT_PUBLIC_POSTHOG_HOST: { class: "plain" },
      }),
    });

    expect(env.posthog).toEqual({
      NEXT_PUBLIC_POSTHOG_KEY: "phc_1",
      NEXT_PUBLIC_POSTHOG_HOST: "https://ph.example.com",
    });
  });

  it("reads an optional group none of whose members was inlined as undefined", () => {
    inlined({});
    const env = defineEnv({
      posthog: group({ NEXT_PUBLIC_POSTHOG_KEY: { class: "plain" } }, { optional: true }),
    });

    expect(env.posthog).toBeUndefined();
  });

  it("refuses a group holding a server-only member as server-only", () => {
    inlined({ NEXT_PUBLIC_POSTHOG_KEY: "phc_1" });
    const env = defineEnv({
      posthog: group({
        NEXT_PUBLIC_POSTHOG_KEY: { class: "plain" },
        POSTHOG_PERSONAL_KEY: { class: "secret" },
      }),
    });

    expect(() => env.posthog).toThrow(/server-only/);
  });

  it("refuses a confidential NEXT_PUBLIC_ member of a group when it is declared", () => {
    expect(() =>
      defineEnv({
        // @ts-expect-error the pairing this asserts on does not typecheck
        posthog: group({
          NEXT_PUBLIC_POSTHOG_KEY: { class: "secret" },
        }),
      }),
    ).toThrow(EnvDefinitionError);
  });
});
