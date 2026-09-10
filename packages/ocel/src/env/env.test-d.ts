import { describe, expectTypeOf, it } from "vitest";
import { z } from "zod";
import { defineEnv, group } from "./index.js";

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

  it("types a scoped variable and a client-accessible one like any other", () => {
    const env = defineEnv({
      TYPED_SCOPED: {
        class: "plain",
        folders: ["/web", "/admin"],
        schema: z.coerce.number(),
      },
      TYPED_CLIENT: { class: "plain", client: true, schema: z.string() },
    });

    expectTypeOf(env.TYPED_SCOPED).toEqualTypeOf<number>();
    expectTypeOf(env.TYPED_CLIENT).toEqualTypeOf<string>();
  });
});

describe("client access on an encrypted class", () => {
  it("does not compile", () => {
    defineEnv({
      // @ts-expect-error an encrypted-baked value is not readable by a browser
      TYPED_CLIENT_SEALED: { class: "sensitive", client: true },
    });
    defineEnv({
      // @ts-expect-error a live value is not readable by a browser
      TYPED_CLIENT_LIVE: { class: "secret", client: true },
    });
    defineEnv({ TYPED_CLIENT_OK: { class: "plain", client: true } });
  });
});

describe("a group of variables", () => {
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
