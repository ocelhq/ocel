import { describe, expectTypeOf, it } from "vitest";
import { link } from "./index.js";

declare module "./index.js" {
  interface LinkProperties {
    "acme:kafka": {
      brokers: string;
      topic: string;
    };
  }
}

describe("typed accessors", () => {
  it("narrows an ocel-owned token to its shipped shape", () => {
    expectTypeOf(link.postgres("main")).toEqualTypeOf<{
      host: string;
      port: string;
      database: string;
      username: string;
      password: string;
    }>();
    expectTypeOf(link.bucket("uploads")).toEqualTypeOf<{ bucket: string }>();
  });

  it("has no method for a token ocel does not own", () => {
    // @ts-expect-error a token typo is an unresolved symbol, never a silent degrade
    link.postgress("main");
  });
});

describe("the raw escape hatch", () => {
  it("degrades an unknown token to a free-form string bag", () => {
    expectTypeOf(link.custom("thing", "acme:redpanda")).toEqualTypeOf<
      Record<string, string>
    >();
  });

  it("types a foreign token declared by module augmentation", () => {
    const kafka = link.custom("events", "acme:kafka");

    expectTypeOf(kafka).toEqualTypeOf<{ brokers: string; topic: string }>();
    expectTypeOf(kafka.brokers).toEqualTypeOf<string>();
    // @ts-expect-error the augmented shape is closed
    kafka.absent;
  });

  it("narrows an ocel-owned token asked for through custom", () => {
    expectTypeOf(link.custom("main", "ocel:bucket")).toEqualTypeOf<{
      bucket: string;
    }>();
  });
});
