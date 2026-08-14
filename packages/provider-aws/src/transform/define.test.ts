import { describe, expect, it } from "vitest";
import { defineTransform } from "./define";
import { surfaceFields } from "./surface";

describe("defineTransform", () => {
  it("normalizes a lone rule into the ordered list the pass consumes", () => {
    const rule = { function: { lambda: { memorySizeMb: 512 } } } as const;

    expect(defineTransform(rule)).toEqual([rule]);
  });

  it("keeps an authored list in the order it was written", () => {
    const first = { function: { lambda: { memorySizeMb: 512 } } } as const;
    const second = { function: { lambda: { memorySizeMb: 1024 } } } as const;

    expect(defineTransform([first, second])).toEqual([first, second]);
  });

  it("exposes only the underlying resources this provider renders", () => {
    expect(Object.keys(surfaceFields.function)).toEqual(["lambda", "url"]);
    expect(Object.keys(surfaceFields.bucket)).toEqual([
      "bucket",
      "cors",
      "listener",
      "notification",
    ]);
    expect(Object.keys(surfaceFields.postgres)).toEqual(["cluster", "instance"]);
  });
});

describe("the authored surface", () => {
  it("accepts a patch, an override that mutates, and an override that returns", () => {
    expect(
      defineTransform([
        { function: { lambda: { memorySizeMb: 512 } } },
        {
          function: {
            lambda: (args) => {
              args.memorySizeMb = args.memorySizeMb * 2;
            },
          },
        },
        { function: { lambda: (args) => ({ ...args, timeoutSeconds: 60 }) } },
      ]),
    ).toHaveLength(3);
  });

  it("hides resource identity from a gate and shows it to an override", () => {
    defineTransform({
      if: (ctx) => {
        // @ts-expect-error a gate sees ambient context only
        return ctx.resourceName === "api-users";
      },
      postgres: { cluster: (args, ctx) => ({ ...args, engineVersion: ctx.resourceName }) },
    });

    expect(true).toBe(true);
  });

  it("rejects an underlying resource this provider does not render", () => {
    defineTransform({
      function: {
        // @ts-expect-error the provider never creates a log group
        logGroup: { retentionDays: 7 },
      },
    });

    expect(true).toBe(true);
  });

  it("rejects a field outside the allowlist, and a value of the wrong shape", () => {
    defineTransform({
      function: {
        lambda: {
          // @ts-expect-error reserved concurrency is not transformable
          reservedConcurrency: 4,
        },
      },
    });

    defineTransform({
      function: {
        lambda: {
          // @ts-expect-error memory is a number of megabytes
          memorySizeMb: "large",
        },
      },
    });

    defineTransform({
      function: {
        lambda: {
          // @ts-expect-error only the runtimes ocel ships are selectable
          runtime: "nodejs18.x",
        },
      },
    });

    expect(true).toBe(true);
  });

  it("rejects an override returning less than the whole args object", () => {
    defineTransform({
      function: {
        // @ts-expect-error an override returns the whole args object or nothing
        lambda: () => ({ timeoutSeconds: 60 }),
      },
    });

    expect(true).toBe(true);
  });
});
