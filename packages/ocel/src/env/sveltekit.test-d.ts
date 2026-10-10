import type { StandardSchemaV1 } from "@standard-schema/spec";
import { describe, expectTypeOf, it } from "vitest";
import { z } from "zod";
import { defineEnvVars } from "./sveltekit.js";

describe("the variables ocel/env/sveltekit hands to SvelteKit", () => {
  it("keeps SvelteKit's typing of each schema, and adds the deployment url", () => {
    const variables = defineEnvVars({
      STRIPE_KEY: { class: "sensitive", description: "Stripe key" },
      PUBLIC_RETRIES: { class: "plain", public: true, schema: z.coerce.number() },
      PORT_HINT: {
        class: "plain",
        schema: (value: string | undefined) => (value === undefined ? 3000 : Number(value)),
      },
    });

    expectTypeOf(variables.PUBLIC_RETRIES.schema).toExtend<{ "~standard": unknown }>();
    expectTypeOf<
      StandardSchemaV1.InferOutput<typeof variables.PORT_HINT.schema>
    >().toEqualTypeOf<number>();
    expectTypeOf(variables.PUBLIC_OCEL_URL.public).toEqualTypeOf<true>();
  });

  it("drops the fields only ocel reads, and reads a sensitive value as a string", () => {
    const variables = defineEnvVars({ STRIPE_KEY: { class: "sensitive", folders: ["/web"] } });

    expectTypeOf<keyof typeof variables.STRIPE_KEY>().toEqualTypeOf<"schema">();
    expectTypeOf<
      StandardSchemaV1.InferOutput<typeof variables.STRIPE_KEY.schema>
    >().toEqualTypeOf<string>();
  });
});

describe("a variable SvelteKit would accept and ocel refuses", () => {
  it("does not compile", () => {
    // @ts-expect-error a public variable may not be sensitive
    defineEnvVars({ PUBLIC_LEAK: { class: "sensitive", public: true } });
    // @ts-expect-error a public variable may not be secret
    defineEnvVars({ PUBLIC_LEAK: { class: "secret", public: true } });
    // @ts-expect-error SvelteKit reads a secret once, so a rotated one never arrives
    defineEnvVars({ SIGNING_KEY: { class: "secret" } });
    // @ts-expect-error a static sensitive value is inlined into the server bundle
    defineEnvVars({ STRIPE_KEY: { class: "sensitive", static: true } });
    defineEnvVars({
      PUBLIC_FINE: { class: "plain", public: true, static: true },
      HIDDEN: { class: "sensitive" },
    });
  });
});
