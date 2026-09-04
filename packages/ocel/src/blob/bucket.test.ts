import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { LinkType } from "../gen/proto/common/links/v1/links_pb.js";
import { z } from "zod";

const declareMock = vi.hoisted(() => vi.fn(() => Promise.resolve({})));

vi.mock("../utils/rpc", () => ({
  rpc: { resource: { declare: declareMock } },
}));

const { bucket } = await import("./bucket.js");
const { uploader } = await import("./uploader.js");

const avatar = uploader(
  { input: z.object({ userId: z.string() }), middleware: ({ input }) => input },
  {},
);

describe("Bucket record", () => {
  afterEach(() => {
    vi.unstubAllEnvs();
  });

  it("refuses its record when this deploy provisioned nothing", () => {
    vi.stubEnv("OCEL_PHASE", "resources-suppressed");

    expect(() => bucket("storage", { uploaders: { avatar } }).__config()).toThrow(
      "'bucket(\"storage\")' cannot be used while resources are suppressed",
    );
  });
});

describe("Bucket discovery declare", () => {
  beforeEach(() => {
    declareMock.mockClear();
  });

  it("declares a BUCKET resource with empty origins by default", () => {
    bucket("storage", { uploaders: { avatar } });

    expect(declareMock).toHaveBeenCalledWith(
      expect.objectContaining({
        resource: { name: "storage", type: LinkType.BUCKET },
        config: { case: "bucket", value: { allowedOrigins: [] } },
      }),
    );
  });

  it("passes through the declared allowedOrigins", () => {
    bucket("storage", {
      allowedOrigins: ["https://app.example.com"],
      uploaders: { avatar },
    });

    expect(declareMock).toHaveBeenCalledWith(
      expect.objectContaining({
        config: {
          case: "bucket",
          value: { allowedOrigins: ["https://app.example.com"] },
        },
      }),
    );
  });
});
