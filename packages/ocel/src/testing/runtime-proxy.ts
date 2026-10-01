import { createServer } from "node:http";
import type { AddressInfo } from "node:net";
import type { ConnectRouter } from "@connectrpc/connect";
import { connectNodeAdapter } from "@connectrpc/connect-node";

export interface RuntimeProxy {
  authorizations: (string | undefined)[];
  close(): Promise<void>;
}

export async function serveRuntimeProxy(
  routes: (router: ConnectRouter) => void,
): Promise<RuntimeProxy> {
  const authorizations: (string | undefined)[] = [];
  const adapter = connectNodeAdapter({ routes });
  const server = createServer((req, res) => {
    authorizations.push(req.headers.authorization);
    adapter(req, res);
  });
  await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", resolve));
  const { port } = server.address() as AddressInfo;
  process.env.OCEL_RUNTIME_ADDRESS = `http://127.0.0.1:${port}`;
  process.env.OCEL_SESSION_TOKEN = "session-token";
  return {
    authorizations,
    close: () =>
      new Promise<void>((resolve, reject) => {
        delete process.env.OCEL_RUNTIME_ADDRESS;
        delete process.env.OCEL_SESSION_TOKEN;
        server.close((err) => (err ? reject(err) : resolve()));
      }),
  };
}
