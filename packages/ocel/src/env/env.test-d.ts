import { describe, expectTypeOf, it } from "vitest";
import { z } from "zod";
import { defineEnv } from "./index.js";

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
});
