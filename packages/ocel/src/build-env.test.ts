import { afterEach, describe, expect, it, vi } from "vitest";
import * as z from "zod";
import { buildEnv, BuildEnvError } from "./build-env.js";

afterEach(() => {
  vi.unstubAllEnvs();
});

describe("buildEnv", () => {
  it("passes a valid value through", () => {
    vi.stubEnv("VPS_HOST", "example.com");
    const env = buildEnv({ VPS_HOST: z.string().min(1) });

    expect(env.VPS_HOST).toBe("example.com");
  });

  it("returns the schema's output for a coercion", () => {
    vi.stubEnv("VPS_PORT", "2222");
    const env = buildEnv({ VPS_PORT: z.coerce.number() });

    expect(env.VPS_PORT).toBe(2222);
  });

  it("returns the schema's output for a transform", () => {
    vi.stubEnv("VPS_HOST", " example.com ");
    const env = buildEnv({
      VPS_HOST: z.string().transform((value) => value.trim()),
    });

    expect(env.VPS_HOST).toBe("example.com");
  });

  it("fills a missing variable from its default", () => {
    const env = buildEnv({ VPS_PORT: z.coerce.number().default(22) });

    expect(env.VPS_PORT).toBe(22);
  });

  it("throws naming a required variable that is missing", () => {
    const env = buildEnv({ VPS_HOST: z.string().min(1) });

    expect(() => env.VPS_HOST).toThrow(BuildEnvError);
    expect(() => env.VPS_HOST).toThrow(/'VPS_HOST'/);
    expect(() => env.VPS_HOST).toThrow(/\.env/);
  });

  it("throws when a set value does not satisfy its schema", () => {
    vi.stubEnv("VPS_HOST", "");
    const env = buildEnv({ VPS_HOST: z.string().min(1) });

    expect(() => env.VPS_HOST).toThrow(BuildEnvError);
  });

  it("throws on a key the call does not declare", () => {
    const env = buildEnv({ VPS_HOST: z.string() }) as Record<string, unknown>;

    expect(() => env.OTHER_KEY).toThrow(BuildEnvError);
    expect(() => env.OTHER_KEY).toThrow(/not declared/);
  });

  it("rejects an unusable key name at call time", () => {
    expect(() => buildEnv({ "vps-host": z.string() })).toThrow(BuildEnvError);
  });

  it("rejects a schema that validates asynchronously", () => {
    vi.stubEnv("VPS_HOST", "example.com");
    const env = buildEnv({
      VPS_HOST: z.string().refine(async () => true),
    });

    expect(() => env.VPS_HOST).toThrow();
  });

  it("resolves a key once", () => {
    let reads = 0;
    vi.stubEnv("VPS_HOST", "example.com");
    const env = buildEnv({
      VPS_HOST: z.string().transform((value) => {
        reads += 1;
        return { value };
      }),
    });

    expect(env.VPS_HOST).toBe(env.VPS_HOST);
    expect(reads).toBe(1);
  });

  it("returns undefined for a symbol property", () => {
    const env = buildEnv({ VPS_HOST: z.string() });

    expect((env as Record<symbol, unknown>)[Symbol.iterator]).toBeUndefined();
  });
});
