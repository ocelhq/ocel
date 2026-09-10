import { fileURLToPath } from "node:url";
import { describe, expect, it, vi } from "vitest";
import { envSchema, sourceOf } from "../src/env/schema.js";
import { BindingType } from "../src/gen/proto/common/bindings/v1/bindings_pb.js";
import { siteOfThisFile } from "./fixtures/callsite/postgres/index.js";

const declareMock = vi.hoisted(() => vi.fn(() => Promise.resolve({})));

vi.mock("../src/utils/rpc", () => ({
  rpc: { resource: { declare: declareMock } },
}));

const { Postgres } = await import("../src/postgres/pg.js");

describe("declarationSite", () => {
  it("names a user file whose path looks like one of the SDK's own modules", () => {
    expect(siteOfThisFile()).toMatch(
      /[/\\]tests[/\\]fixtures[/\\]callsite[/\\]postgres[/\\]index\.ts:\d+$/,
    );
  });

  it("names the caller rather than the SDK module that declared for it", () => {
    const line = new Error().stack?.split("\n")[1]?.match(/:(\d+):\d+\)?$/)?.[1];
    new Postgres("main");

    expect(declareMock).toHaveBeenCalledWith(
      expect.objectContaining({
        resource: { name: "main", type: BindingType.POSTGRES },
        source: `${fileURLToPath(import.meta.url)}:${Number(line) + 1}`,
      }),
    );
  });

  it("names the module a schema was declared in", () => {
    expect(sourceOf(envSchema({ SCHEMA_PORT: { class: "plain" } }))).toContain("callsite.test.ts");
  });
});
