import { describe, expectTypeOf, it } from "vitest";
import { z } from "zod";
import { defineEnv } from "./index.js";
import { declared, envSchema, inlined, type ClientValue } from "./schema.js";

describe("a client accessor typed from envSchema()", () => {
  const schema = envSchema({
    TYPED_PORT: { class: "plain", client: true, schema: z.coerce.number() },
    TYPED_SITE: { class: "plain", client: true },
    TYPED_SERVER_ONLY: { class: "plain", schema: z.url() },
  });

  it("types a schema'd client key as the schema's output, matching the server", () => {
    const env = defineEnv(schema);

    expectTypeOf(inlined(schema, "TYPED_PORT", "1")).toEqualTypeOf<number>();
    expectTypeOf(inlined(schema, "TYPED_PORT", "1")).toEqualTypeOf(env.TYPED_PORT);
    expectTypeOf<ClientValue<typeof schema, "TYPED_PORT">>().toEqualTypeOf<number>();
  });

  it("types a bare client key as a string", () => {
    expectTypeOf(inlined(schema, "TYPED_SITE", "x")).toEqualTypeOf<string>();
    expectTypeOf<ClientValue<typeof schema, "TYPED_SITE">>().toEqualTypeOf<string>();
  });

  it("types a key the schema module never declared as a string", () => {
    expectTypeOf<ClientValue<typeof schema, "TYPED_ABSENT">>().toEqualTypeOf<string>();
  });

  it("finds the envSchema() export of a module by its brand, not its name", () => {
    const module = { whatever: schema, other: 1 };

    expectTypeOf(declared(module)).toEqualTypeOf(schema);
  });

  it("refuses at typecheck a module that exports no envSchema()", () => {
    const module = { plain: { TYPED_PLAIN: { class: "plain" as const } } };

    // @ts-expect-error a module without an envSchema() export cannot back an accessor
    declared(module);
  });

  it("keeps defineEnv's own typing when handed an envSchema()", () => {
    const env = defineEnv(schema);

    expectTypeOf(env.TYPED_PORT).toEqualTypeOf<number>();
    expectTypeOf(env.TYPED_SITE).toEqualTypeOf<string>();
    expectTypeOf(env.TYPED_SERVER_ONLY).toEqualTypeOf<string>();
  });
});
