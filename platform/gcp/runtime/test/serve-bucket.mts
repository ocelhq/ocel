import http from "node:http";
import type { AddressInfo } from "node:net";
import type { CloudStorageBucket } from "./cloud-storage-bucket.mjs";

export interface ServedBucket {
  endpoint: string;
  close(): Promise<void>;
}

export async function serveBucket(bucket: CloudStorageBucket): Promise<ServedBucket> {
  const server = http.createServer((req, res) => {
    void (async () => {
      const answer = await bucket.fetch(`http://storage.test${req.url}`, { method: req.method });
      res.writeHead(answer.status, Object.fromEntries(answer.headers));
      res.end(Buffer.from(await answer.arrayBuffer()));
    })().catch(() => res.destroy());
  });
  await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", resolve));
  return {
    endpoint: `http://127.0.0.1:${(server.address() as AddressInfo).port}`,
    close: () => new Promise<void>((resolve) => server.close(() => resolve())),
  };
}
