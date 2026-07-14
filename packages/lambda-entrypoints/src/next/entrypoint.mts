import { dirname, isAbsolute, relative } from "node:path";
import { pathToFileURL } from "node:url";
import { sendControl, serveInvoke, type Invoke } from "../shared/membrane.mjs";

const waitUntil = (p: Promise<unknown>): void => {
  void Promise.resolve(p).catch(() => {});
};

async function boot(): Promise<void> {
  // OCEL_HANDLER points at the generated launcher beside the app's .next dir,
  // so its dirname is the Next project root and its default export is the
  // compiled route module.
  const handlerPath = process.env.OCEL_HANDLER!;
  const href = isAbsolute(handlerPath) ? pathToFileURL(handlerPath).href : handlerPath;
  const relativeProjectDir = relative(process.cwd(), dirname(handlerPath)) || ".";

  const mod: any = (await import(href)).default;
  const handler = mod?.handler;
  if (typeof handler !== "function") {
    throw new Error(`Next launcher ${handlerPath} does not export a handler function`);
  }

  const invoke: Invoke = (req, res) =>
    handler(req, res, {
      waitUntil,
      requestMeta: { relativeProjectDir, hostname: req.headers.host },
    });

  await serveInvoke(invoke);
}

boot().catch((err) => {
  sendControl("log", { level: "error", message: String(err?.stack || err) });
  process.exit(1);
});
