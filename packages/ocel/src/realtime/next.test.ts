import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { z } from "zod";
import { bindingKey } from "../binding/binding.js";
import { BindingType } from "../gen/proto/common/bindings/v1/bindings_pb.js";
import { appsyncBinding } from "../testing/realtime-gateway.js";

vi.mock("../runtime/rpc", () => ({
  rpc: { resource: { declare: vi.fn(() => Promise.resolve({})) } },
}));

const { realtime, createRealtimeHandler } = await import("./next.js");

const rt = realtime("app", {
  channels: { status: { schema: z.object({ up: z.boolean() }), subscribe: "public" } },
});

describe("the realtime handler on next", () => {
  beforeEach(() => {
    vi.stubEnv("OCEL_PHASE", "");
    vi.stubEnv(bindingKey("app", BindingType.REALTIME), appsyncBinding);
  });

  afterEach(() => {
    vi.unstubAllEnvs();
  });

  it("exports route handlers that serve POST, refuse GET and answer an allowed origin's preflight", async () => {
    const { GET, POST, OPTIONS } = createRealtimeHandler(rt, {
      allowedOrigins: ["https://app.example"],
    });

    const served = await POST(
      new Request("https://shop.example/api/realtime", {
        method: "POST",
        headers: { "content-type": "application/json" },
        body: JSON.stringify({ ops: [{ op: "subscribe", pattern: "status" }] }),
      }),
    );
    const refused = await GET(new Request("https://shop.example/api/realtime"));
    const preflight = await OPTIONS(
      new Request("https://shop.example/api/realtime", {
        method: "OPTIONS",
        headers: { origin: "https://app.example" },
      }),
    );

    expect(served.status).toBe(200);
    expect(((await served.json()) as { grants: unknown[] }).grants).toHaveLength(1);
    expect(refused.status).toBe(405);
    expect(preflight.status).toBe(204);
  });
});
