import { beforeEach, describe, expect, it, vi } from "vitest";
import { LinkType } from "../gen/proto/links/v1/links_pb.js";

const declareMock = vi.hoisted(() => vi.fn(() => Promise.resolve({})));

vi.mock("../utils/rpc", () => ({
  rpc: { resource: { declare: declareMock } },
}));

const { Postgres } = await import("./pg.js");

describe("Postgres discovery declare", () => {
  beforeEach(() => {
    declareMock.mockClear();
  });

  it("defaults to version 17 when no config is given", () => {
    new Postgres("main");

    expect(declareMock).toHaveBeenCalledWith(
      expect.objectContaining({
        resource: { name: "main", type: LinkType.POSTGRES },
        config: { case: "postgres", value: { version: "17" } },
      }),
    );
  });

  it("defaults to version 17 when config omits version", () => {
    new Postgres("main", {});

    expect(declareMock).toHaveBeenCalledWith(
      expect.objectContaining({
        resource: { name: "main", type: LinkType.POSTGRES },
        config: { case: "postgres", value: { version: "17" } },
      }),
    );
  });

  it("passes through a user-supplied version", () => {
    new Postgres("main", { version: "16" });

    expect(declareMock).toHaveBeenCalledWith(
      expect.objectContaining({
        resource: { name: "main", type: LinkType.POSTGRES },
        config: { case: "postgres", value: { version: "16" } },
      }),
    );
  });
});
