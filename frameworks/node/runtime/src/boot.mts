import { type Bind, reportFatalBoot, serveInvoke, serveServer } from "./host.mjs";
import { awaitLiveValues } from "./live-values.mjs";
import { invokeFor, loadUserApp, resolveHandler } from "./user-app.mjs";

export interface BootOptions {
  bind?: () => Bind;
  hooks?: () => void;
}

function handler(env: NodeJS.ProcessEnv): string {
  const declared = env.OCEL_HANDLER;
  if (!declared) {
    throw new Error(
      "ocel: nothing set OCEL_HANDLER, so there is no function for this runtime to serve",
    );
  }
  return declared;
}

async function serve(options: BootOptions): Promise<void> {
  options.hooks?.();
  const address = options.bind?.();
  const entrypoint = handler(process.env);

  await awaitLiveValues();
  const loaded = await loadUserApp(entrypoint);
  if (loaded.kind === "server") {
    await serveServer(loaded.value, undefined, address);
  } else {
    await serveInvoke(invokeFor(resolveHandler(loaded.value)), undefined, address);
  }
}

export function boot(options: BootOptions = {}): void {
  serve(options).catch((err) => {
    reportFatalBoot(err);
    process.exit(1);
  });
}
