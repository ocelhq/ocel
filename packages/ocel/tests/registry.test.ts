import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("../src/runtime/rpc", () => ({
  rpc: { resource: { declare: vi.fn(() => Promise.resolve({})) } },
}));

const { task } = await import("../src/task/index.js");
const { topic } = await import("../src/topic/index.js");
const { worker } = await import("../src/worker/index.js");

describe("declaring a run twice", () => {
  beforeEach(() => {
    vi.stubEnv("OCEL_PHASE", "");
  });

  afterEach(() => {
    vi.unstubAllEnvs();
  });

  it("refuses a second task of the same name declared elsewhere", () => {
    task("twice-task", { run: async () => {} });

    expect(() => task("twice-task", { run: async () => {} })).toThrow(
      'task "twice-task" is declared twice, and a task is declared once',
    );
  });

  it("refuses a second consumer of the same name on one topic declared elsewhere", () => {
    const orders = topic("twice-topic");
    orders.consumer("bill", async () => {});

    expect(() => orders.consumer("bill", async () => {})).toThrow(
      'consumer "bill" of topic "twice-topic" is declared twice, and a consumer is declared once',
    );
  });

  it("refuses a second worker of the same name declared elsewhere", () => {
    worker("twice-worker");

    expect(() => worker("twice-worker")).toThrow(
      'worker "twice-worker" is declared twice, and a worker is declared once',
    );
  });

  it("takes the same declaration evaluated again, as a module loaded twice does", () => {
    const declareAgain = () => task("reloaded-task", { run: async () => {} });
    declareAgain();

    expect(declareAgain).not.toThrow();
  });

  it("takes a second declaration during discovery, which the dev server refuses instead", () => {
    vi.stubEnv("OCEL_PHASE", "discovery");
    worker("discovered-worker");

    expect(() => worker("discovered-worker")).not.toThrow();
  });
});
