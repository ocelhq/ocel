import type http from "node:http";
import { dirname, isAbsolute, relative } from "node:path";
import { pathToFileURL } from "node:url";
import { runWithWaitUntil } from "../shared/background.mjs";
import { revalidatedHeader, revalidationTicks } from "./revalidation-signal.mjs";
import { loadTagsManifest, mirrorTagsInto } from "./tags-manifest.mjs";
import { loadIncrementalCacheFactory } from "./incremental-cache.mjs";
import { originShaping, shapeOriginCache } from "./cache-shaping.mjs";
import { loadProjectManifest } from "./project-manifest.mjs";
import { awaitLiveValues } from "../shared/live-values.mjs";
import { routerMode } from "../shared/edge-kind.mjs";
import {
  installCompileCacheFlush,
  installCompileCacheWarm,
  reportFatalBoot,
  serveEntry,
  serveInvoke,
  serveLocal,
  type Invoke,
} from "../shared/membrane.mjs";

const RSC_REQUEST = Symbol.for("ocel.rsc-request");

function isPprResume(req: http.IncomingMessage): boolean {
  return req.method === "POST" && req.headers["next-resume"] === "1";
}

function announceRevalidations(res: http.ServerResponse): void {
  const before = revalidationTicks();
  const writeHead = res.writeHead;
  res.writeHead = function (this: http.ServerResponse, ...args: any[]) {
    if (!this.headersSent && revalidationTicks() !== before) {
      this.setHeader(revalidatedHeader, "1");
    }
    return (writeHead as any).apply(this, args);
  } as typeof res.writeHead;
}

async function boot(): Promise<void> {
  installCompileCacheFlush();

  const handlerPath = process.env.OCEL_HANDLER!;
  const href = isAbsolute(handlerPath) ? pathToFileURL(handlerPath).href : handlerPath;
  const relativeProjectDir = relative(process.cwd(), dirname(handlerPath)) || ".";

  await awaitLiveValues();

  const mod: any = (await import(href)).default;
  const handler = mod?.handler;
  if (typeof handler !== "function") {
    throw new Error(`Next launcher ${handlerPath} does not export a handler function`);
  }

  installCompileCacheWarm(mod?.warm);

  mirrorTagsInto(loadTagsManifest(dirname(handlerPath)));

  const manifest = await loadProjectManifest(dirname(handlerPath));
  const newIncrementalCache = loadIncrementalCacheFactory(dirname(handlerPath), manifest);
  const shaping = originShaping(manifest, process.env);

  const invoke: Invoke = (req, res, ocel) => {
    if (req.headers.rsc === "1") (req.headers as any)[RSC_REQUEST] = true;
    if (shaping) shapeOriginCache(req, res, shaping);
    announceRevalidations(res);
    if (newIncrementalCache) {
      (globalThis as any).__incrementalCache = newIncrementalCache(req);
    }
    return runWithWaitUntil(ocel.waitUntil, () =>
      handler(req, res, {
        waitUntil: ocel.waitUntil,
        requestMeta: {
          relativeProjectDir,
          hostname: req.headers.host,
          ...(isPprResume(req) && { minimalMode: true }),
        },
      }),
    );
  };

  if (!routerMode(process.env)) {
    await serveInvoke(invoke, (port) => {
      process.env.__NEXT_PRIVATE_ORIGIN = `http://127.0.0.1:${port}`;
    });
    return;
  }

  const localOrigin = `http://127.0.0.1:${await serveLocal(invoke)}`;
  process.env.__NEXT_PRIVATE_ORIGIN = localOrigin;

  const { routerHostFromEnv, routerHostInvoke } = await import("./router-host.mjs");
  await serveEntry(routerHostInvoke(routerHostFromEnv(process.env, localOrigin)));
}

boot().catch((err) => {
  reportFatalBoot(err);
  process.exit(1);
});
