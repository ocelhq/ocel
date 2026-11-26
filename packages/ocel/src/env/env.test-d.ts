import { describe, expectTypeOf, it } from "vitest";
import { z } from "zod";
import { defineEnv, type GroupOptions, group } from "./index.js";

describe("the object defineEnv hands back", () => {
  it("types a schemaless variable as a string and a schema'd one as its output", () => {
    const env = defineEnv({
      TYPED_BARE: { class: "plain" },
      TYPED_PORT: { class: "plain", schema: z.coerce.number() },
    });

    expectTypeOf(env.TYPED_BARE).toEqualTypeOf<string>();
    expectTypeOf(env.TYPED_PORT).toEqualTypeOf<number>();
    // @ts-expect-error a key nothing declared is not on the object
    env.TYPED_ABSENT;
  });

  it("hands back a value, never a promise of one, for every class", () => {
    const env = defineEnv({
      TYPED_SYNC_PLAIN: { class: "plain", schema: z.string() },
      TYPED_SYNC_SEALED: { class: "sensitive", schema: z.coerce.number() },
      TYPED_SYNC_LIVE: { class: "secret" },
    });

    expectTypeOf(env.TYPED_SYNC_PLAIN).toEqualTypeOf<string>();
    expectTypeOf(env.TYPED_SYNC_SEALED).toEqualTypeOf<number>();
    expectTypeOf(env.TYPED_SYNC_LIVE).toEqualTypeOf<string>();

    expectTypeOf(env.TYPED_SYNC_SEALED).not.toExtend<Promise<unknown>>();
    expectTypeOf(env.TYPED_SYNC_LIVE).not.toExtend<PromiseLike<unknown>>();
    expectTypeOf<Awaited<typeof env.TYPED_SYNC_SEALED>>().toEqualTypeOf<
      typeof env.TYPED_SYNC_SEALED
    >();
  });

  it("is read-only: a variable is set through the store, not through the object", () => {
    const env = defineEnv({ TYPED_READONLY: { class: "plain" } });

    expectTypeOf(env).toEqualTypeOf<{ readonly TYPED_READONLY: string }>();
    // @ts-expect-error the object a declaration hands back cannot be written to
    env.TYPED_READONLY = "reassigned";
  });

  it("types a scoped variable like any other", () => {
    const env = defineEnv({
      TYPED_SCOPED: {
        class: "plain",
        folders: ["/web", "/admin"],
        schema: z.coerce.number(),
      },
    });

    expectTypeOf(env.TYPED_SCOPED).toEqualTypeOf<number>();
  });
});

describe("a group of variables", () => {
  it("keeps a group possibly absent when its optional flag is a runtime boolean", () => {
    const options: GroupOptions = { optional: Math.random() > 0.5 };
    const env = defineEnv({
      dynamic: group({ TYPED_DYNAMIC_ID: { class: "plain" } }, options),
    });

    expectTypeOf(env.dynamic).toEqualTypeOf<{ readonly TYPED_DYNAMIC_ID: string } | undefined>();
  });

  it("keeps groups required when options omit or disable optionality", () => {
    const env = defineEnv({
      described: group(
        { TYPED_DESCRIBED_ID: { class: "plain" } },
        { description: "Always required" },
      ),
      emptyOptions: group({ TYPED_EMPTY_OPTIONS_ID: { class: "plain" } }, {}),
      required: group({ TYPED_REQUIRED_ID: { class: "plain" } }, { optional: false }),
    });

    expectTypeOf(env.described).toEqualTypeOf<{ readonly TYPED_DESCRIBED_ID: string }>();
    expectTypeOf(env.emptyOptions).toEqualTypeOf<{ readonly TYPED_EMPTY_OPTIONS_ID: string }>();
    expectTypeOf(env.required).toEqualTypeOf<{ readonly TYPED_REQUIRED_ID: string }>();
  });

  it("types an optional group as possibly absent and a required one as always there", () => {
    const env = defineEnv({
      github: group(
        { TYPED_GITHUB_ID: { class: "plain" }, TYPED_GITHUB_SECRET: { class: "secret" } },
        { optional: true, description: "Enable GitHub sign-in" },
      ),
      smtp: group({ TYPED_SMTP_HOST: { class: "plain" } }),
    });

    expectTypeOf(env.github).toEqualTypeOf<
      { readonly TYPED_GITHUB_ID: string; readonly TYPED_GITHUB_SECRET: string } | undefined
    >();
    expectTypeOf(env.smtp).toEqualTypeOf<{ readonly TYPED_SMTP_HOST: string }>();
  });

  it("flows a member's schema output through the group", () => {
    const env = defineEnv({
      ports: group({ TYPED_GROUP_PORT: { class: "plain", schema: z.coerce.number() } }),
      optionalPorts: group(
        { TYPED_GROUP_OPTIONAL_PORT: { class: "plain", schema: z.coerce.number() } },
        { optional: true },
      ),
    });

    expectTypeOf(env.ports.TYPED_GROUP_PORT).toEqualTypeOf<number>();
    expectTypeOf(env.optionalPorts?.TYPED_GROUP_OPTIONAL_PORT).toEqualTypeOf<number | undefined>();
  });
});
