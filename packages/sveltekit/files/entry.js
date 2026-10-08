import { createReadStream } from "node:fs";
import { Readable } from "node:stream";
import { fileURLToPath } from "node:url";
import { served } from "./served.js";
import { server } from "./server.js";

const IMMUTABLE = "public, max-age=31536000, immutable";
const REVALIDATE = "public, max-age=0, must-revalidate";
const CLIENT_ADDRESS = "x-ocel-client-address";

const root = fileURLToPath(new URL("./static", import.meta.url));

const stream = (file) => Readable.toWeb(createReadStream(file));

await server.init({
  env: process.env,
  read: (file) => stream(`${root}${served.base}/${file}`),
});

function isImmutable(pathname) {
  return served.immutable.some((prefix) => pathname.startsWith(prefix));
}

function clientAddress(request) {
  const carried = request.headers.get(CLIENT_ADDRESS);
  if (carried) return carried;
  return request.headers.get("x-forwarded-for")?.split(",")[0]?.trim() ?? "";
}

function decoded(pathname) {
  try {
    return decodeURIComponent(pathname);
  } catch {
    return pathname;
  }
}

function fromDisk(request, pathname) {
  const redirect = served.redirects[pathname];
  if (redirect) {
    return new Response(null, {
      status: redirect.status,
      headers: { location: redirect.location },
    });
  }
  const asset = served.assets[pathname];
  if (!asset) {
    if (!isImmutable(pathname)) return null;
    return new Response("Not Found", {
      status: 404,
      headers: { "content-type": "text/plain; charset=utf-8", "cache-control": "no-store" },
    });
  }
  const headers = {
    "content-type": asset.type,
    "content-length": String(asset.size),
    "cache-control": isImmutable(pathname) ? IMMUTABLE : REVALIDATE,
    etag: asset.etag,
  };
  if (request.headers.get("if-none-match") === asset.etag) {
    return new Response(null, { status: 304, headers });
  }
  const body = request.method === "HEAD" ? null : stream(`${root}${served.base}/${asset.file}`);
  return new Response(body, { status: 200, headers });
}

export default {
  async fetch(request) {
    if (request.method === "GET" || request.method === "HEAD") {
      const answered = fromDisk(request, decoded(new URL(request.url).pathname));
      if (answered) return answered;
    }
    return server.respond(request, { getClientAddress: () => clientAddress(request) });
  },
};
