import { awaitLiveValues } from "./live-values.mjs";
import {
  installCompileCacheFlush,
  installCompileCacheWarm,
  reportFatalBoot,
  serveInvoke,
  serveServer,
} from "./membrane.mjs";
import { invokeFor, loadUserApp, resolveHandler } from "./user-app.mjs";

async function boot(): Promise<void> {
  installCompileCacheFlush();
  installCompileCacheWarm(undefined);

  await awaitLiveValues();
  const loaded = await loadUserApp(process.env.OCEL_HANDLER!);
  if (loaded.kind === "server") {
    await serveServer(loaded.value);
  } else {
    await serveInvoke(invokeFor(resolveHandler(loaded.value)));
  }
}

boot().catch((err) => {
  reportFatalBoot(err);
  process.exit(1);
});
