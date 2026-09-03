import { mkdtempSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { describe, expect, it } from "vitest";
import { build } from "vite";
import { z } from "zod";

import { EnvClientError } from "./client.js";
import { declared, envSchema, inlined, sourceOf } from "./schema.js";

describe("envSchema", () => {
  it("hands back the definitions it was given, carrying only where they were declared", () => {
    const definitions = {
      SCHEMA_PORT: { class: "plain", client: true, schema: z.coerce.number() },
    } as const;

    const schema = envSchema(definitions);

    expect(schema).toBe(definitions);
    expect(Object.keys(schema)).toEqual(["SCHEMA_PORT"]);
    expect(sourceOf(schema)).toContain("schema.test.ts");
  });

  it("reports no source for definitions declared without it", () => {
    expect(sourceOf({ SCHEMA_BARE: { class: "plain" } })).toBe("");
  });
});

describe("declared", () => {
  it("finds the envSchema() export of a module whatever it is named", () => {
    const schema = envSchema({ SCHEMA_FOUND: { class: "plain", client: true } });

    expect(declared({ other: 1, mine: schema })).toBe(schema);
  });

  it("refuses a module that exports no envSchema(), naming what to do", () => {
    const plain = { SCHEMA_PLAIN: { class: "plain" as const } };

    expect(() => declared({ plain } as never)).toThrow(EnvClientError);
    expect(() => declared({ plain } as never)).toThrow(/envSchema/);
    expect(() => declared({ plain } as never)).toThrow(/defineEnv/);
  });
});

describe("inlined", () => {
  const schema = envSchema({
    SCHEMA_PORT: { class: "plain", client: true, schema: z.coerce.number() },
    SCHEMA_URL: { class: "plain", client: true },
  });

  it("parses an inlined value through its schema", () => {
    expect(inlined(schema, "SCHEMA_PORT", "8080")).toBe(8080);
  });

  it("hands back a bare key's value as the string it was inlined as", () => {
    expect(inlined(schema, "SCHEMA_URL", "https://example.com")).toBe("https://example.com");
  });

  it("hands back a key the schema module does not declare as a string", () => {
    expect(inlined(schema, "SCHEMA_ELSEWHERE", "x")).toBe("x");
  });

  it("refuses a value that fails its schema, naming the key and the complaint", () => {
    expect(() => inlined(schema, "SCHEMA_PORT", "eighty")).toThrow(EnvClientError);
    expect(() => inlined(schema, "SCHEMA_PORT", "eighty")).toThrow(/SCHEMA_PORT/);
    expect(() => inlined(schema, "SCHEMA_PORT", "eighty")).toThrow(/number/i);
  });

  it("refuses a value that was never inlined, naming the key", () => {
    expect(() => inlined(schema, "SCHEMA_PORT", undefined)).toThrow(EnvClientError);
    expect(() => inlined(schema, "SCHEMA_PORT", undefined)).toThrow(/SCHEMA_PORT/);
    expect(() => inlined(schema, "SCHEMA_PORT", undefined)).toThrow(/inlined/);
  });
});

describe("a browser bundle importing only ocel/env/schema", () => {
  it("carries no code that reads, declares or resolves a value", async () => {
    const dir = mkdtempSync(join(tmpdir(), "ocel-env-schema-bundle-"));
    const entry = join(dir, "entry.ts");
    writeFileSync(
      entry,
      `import { declared, envSchema, inlined } from ${JSON.stringify(resolve("src/env/schema.ts"))};\n` +
        `const schema = envSchema({ PUBLIC_ID: { class: "plain", client: true } });\n` +
        `export const id = inlined(declared({ schema }), "PUBLIC_ID", "one");\n`,
    );

    const output = await build({
      root: dir,
      logLevel: "silent",
      build: {
        write: false,
        minify: false,
        lib: { entry, formats: ["es"], fileName: "entry" },
      },
    });
    const [bundle] = Array.isArray(output) ? output : [output];
    const code = ("output" in bundle! ? bundle.output : [])
      .map((chunk) => ("code" in chunk ? chunk.code : ""))
      .join("\n");

    expect(code).toContain("PUBLIC_ID");
    for (const forbidden of [
      "process.env",
      "OCEL_VAR_",
      "OCEL_DEV_SERVER",
      "declareEnv",
      "readLive",
      "readDelivered",
      "liveValues",
      "connect",
    ]) {
      expect(code).not.toContain(forbidden);
    }
  });
});
