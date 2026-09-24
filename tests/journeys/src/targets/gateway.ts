import {
  Agent,
  createServer as createHttpServer,
  request as httpRequest,
  type Server,
} from "node:http";

const EDGE_PORT = 80;

export type Gateway = {
  serving: (hostname: string) => Promise<string>;
  close: () => Promise<void>;
};

function listening(server: Server): Promise<string> {
  return new Promise((resolve, reject) => {
    server.on("error", reject);
    server.listen(0, "127.0.0.1", () => {
      const address = server.address();
      if (typeof address !== "object" || address === null) {
        reject(new Error("the gateway could not read the port the kernel handed out"));
        return;
      }
      resolve(`http://127.0.0.1:${address.port}`);
    });
  });
}

function closing(server: Server): Promise<void> {
  return new Promise((resolve) => {
    server.closeAllConnections();
    server.close(() => resolve());
  });
}

export type Edge = { host: string; port: number };

export function forwarder(edge: Edge, hostname: string): Server {
  const unpooled = new Agent({ keepAlive: false });
  const server = createHttpServer((from, to) => {
    const upstream = httpRequest(
      {
        host: edge.host,
        port: edge.port,
        agent: unpooled,
        method: from.method,
        path: from.url,
        headers: { ...from.headers, host: hostname },
      },
      (answered) => {
        to.writeHead(answered.statusCode ?? 502, answered.headers);
        answered.pipe(to);
      },
    );
    upstream.on("error", (error) => {
      to.writeHead(502, { "content-type": "text/plain" }).end(String(error));
    });
    from.pipe(upstream);
  });
  server.on("close", () => unpooled.destroy());
  return server;
}

export function openGateway(box: string): Gateway {
  const servers: Server[] = [];
  const forwarders = new Map<string, Promise<string>>();

  return {
    serving(hostname) {
      let url = forwarders.get(hostname);
      if (!url) {
        const server = forwarder({ host: box, port: EDGE_PORT }, hostname);
        servers.push(server);
        url = listening(server);
        forwarders.set(hostname, url);
      }
      return url;
    },
    close: async () => {
      await Promise.all(servers.map(closing));
    },
  };
}
