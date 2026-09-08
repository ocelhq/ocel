import { awaitLiveValues } from "./live-values.mjs";
import { type Bind, reportFatalBoot, serveInvoke, serveServer } from "./membrane.mjs";
import { invokeFor, loadUserApp, resolveHandler } from "./user-app.mjs";

const everyNetwork = "0.0.0.0";

function bind(env: NodeJS.ProcessEnv): Bind {
  const declared = env.PORT;
  if (!declared) {
    throw new Error("ocel: nothing set PORT, so there is no port to serve this function on");
  }
  const port = Number(declared);
  if (!Number.isInteger(port) || port < 1 || port > 65535) {
    throw new Error(`ocel: PORT is ${declared}, which is not a port to serve this function on`);
  }
  return { host: everyNetwork, port };
}

async function boot(): Promise<void> {
  const address = bind(process.env);
  await awaitLiveValues();
  const loaded = await loadUserApp(process.env.OCEL_HANDLER!);
  if (loaded.kind === "server") {
    await serveServer(loaded.value, undefined, address);
  } else {
    await serveInvoke(invokeFor(resolveHandler(loaded.value)), undefined, address);
  }
}

boot().catch((err) => {
  reportFatalBoot(err);
  process.exit(1);
});
