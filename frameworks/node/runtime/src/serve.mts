import { boot } from "./boot.mjs";
import type { Bind } from "./host.mjs";

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

boot({ bind: () => bind(process.env) });
