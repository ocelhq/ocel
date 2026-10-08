import http from "node:http";
import type { AddressInfo } from "node:net";
import { gzipSync } from "node:zlib";
import { CLIENT_AUTHORIZATION_HEADER } from "@platform/edge-contract/client-authorization";
import { expect, test } from "vitest";
import { siblingOriginFetch } from "../src/next/dispatch-signing.mjs";

const SIBLING_URL = "https://abc123.lambda-url.us-east-1.on.aws/sibling";

const credentials = {
  AWS_ACCESS_KEY_ID: "AKIAEXAMPLE",
  AWS_SECRET_ACCESS_KEY: "secret",
};

async function forwarded(headers: Record<string, string>): Promise<Request> {
  const sent: Request[] = [];
  const doFetch = (async (input: Request) => {
    sent.push(new Request(input));
    return new Response("sibling");
  }) as unknown as typeof fetch;
  await siblingOriginFetch(
    credentials,
    "us-east-1",
    doFetch,
  )(new Request(SIBLING_URL, { headers }));
  return sent[0]!;
}

function answering(response: Response): Promise<Response> {
  const doFetch = (async () => response) as unknown as typeof fetch;
  return siblingOriginFetch(credentials, "us-east-1", doFetch)(SIBLING_URL);
}

test("a sibling call hands the client's authorization in the carrier, since SigV4 takes authorization", async () => {
  const sent = await forwarded({
    authorization: "Bearer client-token",
    [CLIENT_AUTHORIZATION_HEADER]: "Bearer forged",
  });

  expect(sent.headers.get("authorization")).toContain("AWS4-HMAC-SHA256");
  expect(sent.headers.get(CLIENT_AUTHORIZATION_HEADER)).toBe("Bearer client-token");
});

test("a sibling call drops a carrier the client sent when the client sent no authorization", async () => {
  const sent = await forwarded({ [CLIENT_AUTHORIZATION_HEADER]: "Bearer forged" });

  expect(sent.headers.has(CLIENT_AUTHORIZATION_HEADER)).toBe(false);
});

test("a sibling's remapped WWW-Authenticate is restored, and no remapped name is handed on", async () => {
  const answered = await answering(
    new Response("denied", {
      status: 401,
      headers: {
        "x-amzn-remapped-www-authenticate": 'Bearer realm="app"',
        "x-amzn-remapped-date": "Mon, 01 Jan 2024 00:00:00 GMT",
        "x-amzn-remapped-connection": "close",
        date: "Tue, 29 Sep 2026 00:00:00 GMT",
      },
    }),
  );

  expect(answered.status).toBe(401);
  expect(await answered.text()).toBe("denied");
  expect(answered.headers.get("www-authenticate")).toBe('Bearer realm="app"');
  expect(answered.headers.get("date")).toBe("Tue, 29 Sep 2026 00:00:00 GMT");
  expect(answered.headers.get("connection")).toBeNull();
  expect(
    [...answered.headers.keys()].filter((name) => name.startsWith("x-amzn-remapped-")),
  ).toEqual([]);
});

test("a loopback render the app gzipped itself comes back with its own bytes and encoding", async () => {
  const gzipped = gzipSync(Buffer.from("compressed by the app"));
  const local = http.createServer((_req, res) => {
    res.writeHead(200, { "content-encoding": "gzip", "content-length": gzipped.length });
    res.end(gzipped);
  });
  await new Promise<void>((resolve) => local.listen({ host: "127.0.0.1", port: 0 }, resolve));
  try {
    const { port } = local.address() as AddressInfo;
    const response = await siblingOriginFetch(
      credentials,
      "us-east-1",
    )(new Request(`http://127.0.0.1:${port}/page`, { headers: { "accept-encoding": "gzip" } }));

    expect(response.headers.get("content-encoding")).toBe("gzip");
    expect(Buffer.from(await response.arrayBuffer())).toEqual(gzipped);
  } finally {
    await new Promise<void>((resolve) => local.close(() => resolve()));
  }
});
