import { Code, ConnectError } from "@connectrpc/connect";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { TaskService } from "../gen/proto/app/task/v1/task_pb.js";
import { TopicService } from "../gen/proto/app/topic/v1/topic_pb.js";
import { type RuntimeProxy, serveRuntimeProxy } from "../testing/runtime-proxy.js";

vi.mock("./rpc", () => ({
  rpc: { resource: { declare: vi.fn(() => Promise.resolve({})) } },
}));

const { topic, RuntimeCallError } = await import("../topic/index.js");
const { task } = await import("../task/index.js");

let proxy: RuntimeProxy;

beforeEach(async () => {
  vi.stubEnv("OCEL_PHASE", "");
  vi.stubEnv("OCEL_RESOURCE_TOPIC_orders", JSON.stringify({ name: "orders", topic: {} }));
  vi.stubEnv("OCEL_RESOURCE_TASK_resize", JSON.stringify({ name: "resize", task: {} }));
  proxy = await serveRuntimeProxy((router) => {
    router.service(TopicService, {
      send: () => {
        throw new ConnectError("the topic is not served here", Code.NotFound);
      },
    });
    router.service(TaskService, {
      trigger: () => {
        throw new ConnectError("the queue is full", Code.ResourceExhausted);
      },
    });
  });
});

afterEach(async () => {
  vi.unstubAllEnvs();
  await proxy.close();
});

it("refuses a send the runtime refused with a RuntimeCallError carrying its code and its cause", async () => {
  const sent = topic("orders").send({ id: "o1" });

  await expect(sent).rejects.toBeInstanceOf(RuntimeCallError);
  await expect(sent).rejects.toMatchObject({
    code: "not_found",
    procedure: "app.topic.v1.TopicService/Send",
    message: expect.stringContaining("the topic is not served here"),
    cause: expect.any(ConnectError),
  });
});

it("refuses a trigger the runtime refused with a RuntimeCallError", async () => {
  const triggered = task("resize", { run: async () => {} }).trigger({});

  await expect(triggered).rejects.toMatchObject({ code: "resource_exhausted" });
  await expect(triggered).rejects.toBeInstanceOf(RuntimeCallError);
});
