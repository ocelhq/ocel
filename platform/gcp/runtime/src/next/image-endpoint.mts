import type { ImageOriginRequest } from "@framework/next-router/image";
import { originImagePath } from "@framework/next-router/image";
import { fetchToNodeHandler } from "@framework/node-runtime/fetch-bridge";
import type { Invoke } from "@framework/node-runtime/host";
import { newDiskObjectStore } from "./disk-assets.mjs";
import { newInProcessImageOrigin } from "./image-origin.mjs";

const maxBodyBytes = 16 * 1024;

const staticDirVar = "OCEL_STATIC_DIR";

export function newImageEndpointInvoke(next: Invoke, env: NodeJS.ProcessEnv): Invoke {
  const staticDir = env[staticDirVar];
  if (!staticDir) return next;
  const optimize = newInProcessImageOrigin(
    newDiskObjectStore(staticDir, env.OCEL_ASSET_PREFIX ?? ""),
  );
  const answer = fetchToNodeHandler(async (request) => {
    if (request.method !== "POST") {
      return text(405, "POST the image request to this path.", { allow: "POST" });
    }
    const body = await readCapped(request, maxBodyBytes);
    if (body === undefined) return text(413, "The image request is too large.");
    const payload = parseObject(body);
    if (payload === undefined) return text(400, "The image request is not a JSON object.");
    return optimize(payload as unknown as ImageOriginRequest);
  });
  return (req, res, ocel) => {
    const path = new URL(req.url ?? "/", "http://n").pathname;
    return path === originImagePath ? answer(req, res, ocel) : next(req, res, ocel);
  };
}

function text(status: number, message: string, headers: Record<string, string> = {}): Response {
  return new Response(message, {
    status,
    headers: { "content-type": "text/plain; charset=utf-8", ...headers },
  });
}

async function readCapped(request: Request, limit: number): Promise<string | undefined> {
  const reader = request.body?.getReader();
  if (!reader) return "";
  const chunks: Uint8Array[] = [];
  let size = 0;
  for (;;) {
    const { done, value } = await reader.read();
    if (done) break;
    size += value.byteLength;
    if (size > limit) {
      await reader.cancel();
      return undefined;
    }
    chunks.push(value);
  }
  return Buffer.concat(chunks).toString("utf8");
}

function parseObject(body: string): Record<string, unknown> | undefined {
  try {
    const parsed: unknown = JSON.parse(body);
    return typeof parsed === "object" && parsed !== null && !Array.isArray(parsed)
      ? (parsed as Record<string, unknown>)
      : undefined;
  } catch {
    return undefined;
  }
}
