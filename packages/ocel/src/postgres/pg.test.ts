import { beforeEach, describe, expect, it, vi } from "vitest";

const declareMock = vi.hoisted(() => vi.fn(() => Promise.resolve({})));

vi.mock("../utils/rpc", () => ({
  rpc: { resource: { declare: declareMock } },
  ResourceType: { POSTGRES: 1 },
}));

const { Postgres } = await import("./pg");

describe("Postgres discovery declare", () => {
  beforeEach(() => {
    declareMock.mockClear();
  });

  it("defaults to version 17 when no config is given", () => {
    new Postgres("main");

    expect(declareMock).toHaveBeenCalledWith(
      expect.objectContaining({
        config: { case: "postgres", value: { version: "17" } },
      }),
    );
  });

  it("defaults to version 17 when config omits version", () => {
    new Postgres("main", {});

    expect(declareMock).toHaveBeenCalledWith(
      expect.objectContaining({
        config: { case: "postgres", value: { version: "17" } },
      }),
    );
  });

  it("passes through a user-supplied version", () => {
    new Postgres("main", { version: "16" });

    expect(declareMock).toHaveBeenCalledWith(
      expect.objectContaining({
        config: { case: "postgres", value: { version: "16" } },
      }),
    );
  });
});
