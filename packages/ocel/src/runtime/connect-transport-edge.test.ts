import { afterEach, expect, it } from "vitest";
import { createBucketClient } from "../bucket/bucket-client.js";
import { BucketService, UploadState } from "../gen/proto/app/bucket/v1/bucket_pb.js";
import { type RuntimeProxy, serveRuntimeProxy } from "../testing/runtime-proxy.js";
import { createConnectTransport } from "./connect-transport-edge.js";

let proxy: RuntimeProxy | undefined;

afterEach(async () => {
  await proxy?.close();
  proxy = undefined;
});

it("calls the ocel runtime over fetch, presenting what its interceptors set", async () => {
  const sessions: string[] = [];
  proxy = await serveRuntimeProxy((router) =>
    router.service(BucketService, {
      getUploadStatus: (req) => {
        sessions.push(req.sessionId);
        return { state: UploadState.SUCCEEDED, error: "" };
      },
    }),
  );
  const client = createBucketClient(
    createConnectTransport({
      baseUrl: process.env.OCEL_RUNTIME_ADDRESS!,
      interceptors: [
        (next) => (req) => {
          req.header.set("Authorization", "Bearer session-token");
          return next(req);
        },
      ],
    }),
  );

  const status = await client.getUploadStatus({ sessionId: "upload-1" });

  expect(status.state).toBe(UploadState.SUCCEEDED);
  expect(sessions).toEqual(["upload-1"]);
  expect(proxy.authorizations).toEqual(["Bearer session-token"]);
});

it("reads a response the ocel runtime gzipped, which fetch has already decompressed", async () => {
  const error = "upload expired ".repeat(200);
  proxy = await serveRuntimeProxy((router) =>
    router.service(BucketService, {
      getUploadStatus: () => ({ state: UploadState.EXPIRED, error }),
    }),
  );
  const client = createBucketClient(
    createConnectTransport({ baseUrl: process.env.OCEL_RUNTIME_ADDRESS!, interceptors: [] }),
  );

  const status = await client.getUploadStatus({ sessionId: "upload-1" });

  expect(status.state).toBe(UploadState.EXPIRED);
  expect(status.error).toBe(error);
});
