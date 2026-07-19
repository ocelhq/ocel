import { dirname, isAbsolute, relative } from "node:path";
import { pathToFileURL } from "node:url";
import { runWithWaitUntil } from "../shared/background.mjs";
import { reportFatalBoot, serveInvoke, type Invoke } from "../shared/membrane.mjs";

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

  // Next's cache handlers are loaded as their own module graphs and cannot see
  // this context, so the invocation's waitUntil is also published through the
  // background bridge — which is how a handler defers work onto the request it
  // is serving without the request waiting for it.
  const invoke: Invoke = (req, res, ocel) =>
    runWithWaitUntil(ocel.waitUntil, () =>
      handler(req, res, {
        waitUntil: ocel.waitUntil,
        requestMeta: { relativeProjectDir, hostname: req.headers.host },
      }),
    );

  await serveInvoke(invoke);
}

boot().catch((err) => {
  reportFatalBoot(err);
  process.exit(1);
});
