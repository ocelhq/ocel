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
const { task } = await import("../src/task/index.js");
const { topic } = await import("../src/topic/index.js");
const { worker } = await import("../src/worker/index.js");

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

  it("names the line a task, topic, consumer and worker were declared on", () => {
    const line = Number(new Error().stack?.split("\n")[1]?.match(/:(\d+):\d+\)?$/)?.[1]);
    task("callsite-task", { run: async () => {} });
    const orders = topic("callsite-topic");
    orders.consumer("callsite-consumer", async () => {});
    worker("callsite-worker");

    const here = fileURLToPath(import.meta.url);
    const sources = (
      declareMock.mock.calls as unknown as [{ resource: { name: string }; source: string }][]
    )
      .map(([req]) => req)
      .filter((req) => req.resource.name.startsWith("callsite-"))
      .map((req) => [req.resource.name, req.source]);
    expect(sources).toEqual([
      ["callsite-task", `${here}:${line + 1}`],
      ["callsite-topic", `${here}:${line + 2}`],
      ["callsite-consumer", `${here}:${line + 3}`],
      ["callsite-worker", `${here}:${line + 4}`],
    ]);
  });

  it("names the module a schema was declared in", () => {
    expect(sourceOf(envSchema({ SCHEMA_PORT: { class: "plain" } }))).toContain("callsite.test.ts");
  });
});
