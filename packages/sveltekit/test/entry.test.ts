import { copyFileSync, mkdirSync, mkdtempSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { pathToFileURL } from "node:url";
import { beforeAll, describe, expect, it } from "vitest";

const IMMUTABLE = "public, max-age=31536000, immutable";
const REVALIDATE = "public, max-age=0, must-revalidate";

type Handler = { fetch(request: Request): Promise<Response> };

let app: Handler;

beforeAll(async () => {
  const dir = mkdtempSync(join(tmpdir(), "ocel-sveltekit-entry-"));
  copyFileSync(new URL("../files/entry.js", import.meta.url), join(dir, "entry.js"));
  writeFileSync(
    join(dir, "server.js"),
    `export const server = {
      async init({ read }) { globalThis.read = read; },
      async respond(request, { getClientAddress }) {
        return Response.json({ rendered: new URL(request.url).pathname, client: getClientAddress() });
      },
    };\n`,
  );
  writeFileSync(
    join(dir, "served.js"),
    `export const served = ${JSON.stringify({
      base: "",
      immutable: ["/_app/immutable/"],
      assets: {
        "/ocel.svg": { file: "ocel.svg", type: "image/svg+xml", size: 6, etag: '"svg"' },
        "/_app/immutable/start.js": {
          file: "_app/immutable/start.js",
          type: "text/javascript; charset=utf-8",
          size: 5,
          etag: '"js"',
        },
      },
      redirects: { "/old": { status: 308, location: "/new" } },
    })};\n`,
  );
  mkdirSync(join(dir, "static", "_app", "immutable"), { recursive: true });
  writeFileSync(join(dir, "static", "ocel.svg"), "<svg/>");
  writeFileSync(join(dir, "static", "_app", "immutable", "start.js"), "start");
  app = (await import(pathToFileURL(join(dir, "entry.js")).href)).default;
});

const get = (path: string, headers: Record<string, string> = {}) =>
  app.fetch(new Request(`https://app.example${path}`, { headers }));

describe("the function entry", () => {
  it("serves a static file with its stated type, length and revalidation", async () => {
    const res = await get("/ocel.svg");
    expect(res.status).toBe(200);
    expect(await res.text()).toBe("<svg/>");
    expect(res.headers.get("content-type")).toBe("image/svg+xml");
    expect(res.headers.get("content-length")).toBe("6");
    expect(res.headers.get("cache-control")).toBe(REVALIDATE);
  });

  it("serves a content-hashed chunk as immutable", async () => {
    const res = await get("/_app/immutable/start.js");
    expect(res.headers.get("cache-control")).toBe(IMMUTABLE);
  });

  it("answers a miss under an immutable prefix itself, uncached, rather than rendering it", async () => {
    const res = await get("/_app/immutable/gone.js");
    expect(res.status).toBe(404);
    expect(res.headers.get("cache-control")).toBe("no-store");
  });

  it("answers a matching If-None-Match with 304", async () => {
    const res = await get("/ocel.svg", { "if-none-match": '"svg"' });
    expect(res.status).toBe(304);
  });

  it("redirects a path kit prerendered as a redirect", async () => {
    const res = await get("/old");
    expect(res.status).toBe(308);
    expect(res.headers.get("location")).toBe("/new");
  });

  it("renders every other path with the client address ocel carries", async () => {
    const res = await get("/blog", {
      "x-ocel-client-address": "203.0.113.7",
      "x-forwarded-for": "198.51.100.1",
    });
    expect(await res.json()).toEqual({ rendered: "/blog", client: "203.0.113.7" });
  });

  it("renders a POST to a static path rather than serving the file", async () => {
    const res = await app.fetch(
      new Request("https://app.example/ocel.svg", { method: "POST", body: "x" }),
    );
    expect(await res.json()).toMatchObject({ rendered: "/ocel.svg" });
  });

  it("hands kit's read() files from the static copy beside it", async () => {
    const read = (globalThis as unknown as { read: (file: string) => ReadableStream }).read;
    expect(await new Response(read("ocel.svg")).text()).toBe("<svg/>");
  });
});
