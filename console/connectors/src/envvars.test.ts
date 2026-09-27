import { createServer, type Server } from "node:http";
import type { ConnectRouter } from "@connectrpc/connect";
import { connectNodeAdapter } from "@connectrpc/connect-node";
import { afterEach, describe, expect, it } from "vitest";
import { createEnvSourceValue, describeEnvSource, list } from "./envvars";
import {
  type CreateEnvSourceValueRequest,
  EnvVarsService,
} from "./gen/provider/envvars/v1/envvars_pb";
import type { Connector } from "./transport";

let server: Server | undefined;

afterEach(() => server?.close());

function serving(routes: (router: ConnectRouter) => void): Promise<Connector> {
  server = createServer(connectNodeAdapter({ routes }));
  return new Promise((resolve) => {
    server?.listen(0, "127.0.0.1", () => {
      const address = server?.address();
      const port = typeof address === "object" && address !== null ? address.port : 0;
      resolve({ id: "conn-1", url: `http://127.0.0.1:${port}`, token: "t", capabilities: [] });
    });
  });
}

describe("an env source through the connector", () => {
  it("names the env source a stored value was copied from", async () => {
    const connector = await serving((router) =>
      router.service(EnvVarsService, {
        listValues: () => ({
          values: [
            {
              coordinate: { slug: "shop", key: "API", folder: "" },
              version: 2n,
              envSource: "exec",
            },
            { coordinate: { slug: "shop", key: "LOG", folder: "" }, version: 1n },
          ],
        }),
      }),
    );
    const answer = await list(connector, "production", "shop");
    expect(answer.done && answer.result.map((value) => value.envSource)).toEqual([
      "exec",
      undefined,
    ]);
  });

  it("describes the env source a tier reads from, with a URL per folder", async () => {
    const connector = await serving((router) =>
      router.service(EnvVarsService, {
        describeEnvSource: () => ({
          status: {
            envSource: "infisical:p-1/prod",
            writable: true,
            links: [{ folder: "", url: "https://infisical.example/root" }],
            credentials: ["INFISICAL_CLIENT_ID"],
          },
        }),
      }),
    );
    expect(await describeEnvSource(connector, "production", "shop")).toEqual({
      done: true,
      result: {
        id: "infisical:p-1/prod",
        writable: true,
        urls: { "": "https://infisical.example/root" },
        credentials: ["INFISICAL_CLIENT_ID"],
      },
    });
  });

  it("describes a tier nothing registered as builtin", async () => {
    const connector = await serving((router) =>
      router.service(EnvVarsService, { describeEnvSource: () => ({ status: {} }) }),
    );
    const answer = await describeEnvSource(connector, "preview", "shop");
    expect(answer.done && answer.result).toEqual({
      id: "builtin",
      writable: false,
      urls: {},
      credentials: [],
    });
  });

  it("creates a missing value with its description and says when it waits for approval", async () => {
    const seen: CreateEnvSourceValueRequest[] = [];
    const connector = await serving((router) =>
      router.service(EnvVarsService, {
        createEnvSourceValue: (request) => {
          seen.push(request);
          return { awaitingApproval: true };
        },
      }),
    );
    const answer = await createEnvSourceValue(
      connector,
      "production",
      "shop",
      { key: "API", folder: "/web", environment: "" },
      "https://api.example",
      "where the API lives",
    );
    expect(answer).toEqual({ done: true, result: { awaitingApproval: true } });
    expect(
      seen.map((request) => [
        request.coordinate?.key,
        request.coordinate?.folder,
        request.value,
        request.description,
      ]),
    ).toEqual([["API", "/web", "https://api.example", "where the API lives"]]);
  });
});
