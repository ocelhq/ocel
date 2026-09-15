import { createServer } from "node:http";
import type { AddressInfo } from "node:net";
import { afterEach, expect, it, vi } from "vitest";

afterEach(() => {
  vi.unstubAllEnvs();
  vi.resetModules();
});

it("presents the dev server token the CLI handed the discovery child", async () => {
  const seen: (string | undefined)[] = [];
  const server = createServer((req, res) => {
    seen.push(req.headers.authorization);
    res.statusCode = 403;
    res.end();
  });
  await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", resolve));
  const { port } = server.address() as AddressInfo;

  vi.stubEnv("OCEL_DEV_SERVER", `http://127.0.0.1:${port}`);
  vi.stubEnv("OCEL_DEV_SERVER_TOKEN", "dev-server-token");
  vi.resetModules();
  const { rpc } = await import("./rpc.js");

  await expect(rpc.resource.declare({})).rejects.toThrow();

  await new Promise<void>((resolve, reject) =>
    server.close((err) => (err ? reject(err) : resolve())),
  );
  expect(seen).toEqual(["Bearer dev-server-token"]);
});
