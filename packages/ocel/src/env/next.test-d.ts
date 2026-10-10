import { describe, expectTypeOf, it } from "vitest";
import { z } from "zod";
import { defineEnv, group } from "./next.js";

describe("the object ocel/env/next hands back", () => {
  it("types each variable by its schema, and by string where it has none", () => {
    const env = defineEnv({
      STRIPE_KEY: { class: "secret" },
      NEXT_PUBLIC_API_URL: { class: "plain", schema: z.string().url() },
      NEXT_PUBLIC_RETRIES: { class: "plain", schema: z.coerce.number() },
      NEXT_PUBLIC_SITE: { class: "plain" },
    });

    expectTypeOf(env.STRIPE_KEY).toEqualTypeOf<string>();
    expectTypeOf(env.NEXT_PUBLIC_API_URL).toEqualTypeOf<string>();
    expectTypeOf(env.NEXT_PUBLIC_RETRIES).toEqualTypeOf<number>();
    expectTypeOf(env.NEXT_PUBLIC_SITE).toEqualTypeOf<string>();
  });

  it("types a group of public variables as one object", () => {
    const env = defineEnv({
      posthog: group({
        NEXT_PUBLIC_POSTHOG_KEY: { class: "plain" },
        NEXT_PUBLIC_POSTHOG_SAMPLE: { class: "plain", schema: z.coerce.number() },
      }),
    });

    expectTypeOf(env.posthog).toEqualTypeOf<{
      readonly NEXT_PUBLIC_POSTHOG_KEY: string;
      readonly NEXT_PUBLIC_POSTHOG_SAMPLE: number;
    }>();
  });
});

describe("a NEXT_PUBLIC_ variable of a confidential class", () => {
  it("does not compile", () => {
    defineEnv({
      // @ts-expect-error Next inlines a NEXT_PUBLIC_ value into the browser bundle
      NEXT_PUBLIC_SENSITIVE: { class: "sensitive" },
    });
    defineEnv({
      // @ts-expect-error Next inlines a NEXT_PUBLIC_ value into the browser bundle
      NEXT_PUBLIC_SECRET: { class: "secret" },
    });
    defineEnv({
      // @ts-expect-error a group member is inlined into the browser bundle too
      members: group({
        NEXT_PUBLIC_MEMBER: { class: "secret" },
      }),
    });
    defineEnv({ NEXT_PUBLIC_PLAIN: { class: "plain" }, PRIVATE_SECRET: { class: "secret" } });
  });
});
