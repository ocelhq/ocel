import { createServer, type Server } from "node:http";
import { afterEach, expect, it } from "vitest";
import { vars } from "./transport";

let server: Server | undefined;

afterEach(() => server?.close());

function answering(seen: string[]): Promise<string> {
  server = createServer((request, response) => {
    seen.push(request.headers.authorization ?? "");
    request.resume();
    request.on("end", () => {
      response.writeHead(200, { "Content-Type": "application/proto" });
      response.end();
    });
  });
  return new Promise((resolve) => {
    server?.listen(0, "127.0.0.1", () => {
      const held = server?.address();
      resolve(typeof held === "object" && held !== null ? `http://127.0.0.1:${held.port}` : "");
    });
  });
}

it("carries the token as a bearer credential", async () => {
  const seen: string[] = [];
  const url = await answering(seen);

  await vars({ id: "conn-1", url, token: "minted.jwt.value", capabilities: [] }).listValues({
    slug: "shop",
  });

  expect(seen).toEqual(["Bearer minted.jwt.value"]);
});
